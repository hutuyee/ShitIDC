package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/hutuyee/ShitIDC/internal/cache"
	"github.com/hutuyee/ShitIDC/internal/config"
	"github.com/hutuyee/ShitIDC/internal/database"
	"github.com/hutuyee/ShitIDC/internal/queue"
	"github.com/hutuyee/ShitIDC/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	db, err := database.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	rdb, err := cache.Open(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		log.Fatal(err)
	}
	defer rdb.Close()
	st := store.New(db).WithMasterKey(cfg.MasterKey)
	q := queue.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer q.Close()

	// withLock runs fn only when this scheduler owns the named Redis lock,
	// so multi-node deployments never run the same job twice.
	withLock := func(lockKey string, ttl time.Duration, fn func(context.Context)) {
		owner := uuid.NewString()
		locked, err := rdb.SetNX(ctx, lockKey, owner, ttl).Result()
		if err != nil || !locked {
			return
		}
		defer func() {
			if v, _ := rdb.Get(ctx, lockKey).Result(); v == owner {
				_ = rdb.Del(ctx, lockKey).Err()
			}
		}()
		fn(ctx)
	}

	c := cron.New()
	_, _ = c.AddFunc("@every 1m", func() {
		withLock("scheduler:pending-service-requeue", 50*time.Second, func(ctx context.Context) {
			ids, err := st.PendingServiceIDs(ctx, 500)
			if err != nil {
				log.Printf("load pending services: %v", err)
				return
			}
			for _, id := range ids {
				if err := q.Provision(id); err != nil {
					log.Printf("requeue service %s: %v", id, err)
				}
			}
		})
	})
	_, _ = c.AddFunc("0 * * * *", func() {
		if err := q.ProviderSync(); err != nil {
			log.Printf("enqueue provider sync: %v", err)
		}
	})
	_, _ = c.AddFunc("@every 1m", func() {
		withLock("scheduler:order-expire", 50*time.Second, func(ctx context.Context) {
			n, err := st.CancelExpiredOrders(ctx, 24)
			if err != nil {
				log.Printf("cancel expired orders: %v", err)
				return
			}
			if n > 0 {
				log.Printf("auto-cancelled %d expired unpaid order(s)", n)
			}
		})
	})
	// Roadmap 第二十阶段: 到期检测 + 自动暂停。Active services past their
	// expiry are suspended; the worker claim makes duplicate enqueues no-ops.
	_, _ = c.AddFunc("@every 1m", func() {
		withLock("scheduler:service-expire", 50*time.Second, func(ctx context.Context) {
			refs, err := st.ExpiredActiveServices(ctx, 200)
			if err != nil {
				log.Printf("load expired services: %v", err)
				return
			}
			for _, ref := range refs {
				if err := q.ServiceSuspend(ref.PublicID); err != nil {
					log.Printf("enqueue suspend %s: %v", ref.PublicID, err)
					continue
				}
				log.Printf("auto-suspending expired service %s", ref.PublicID)
			}
		})
	})
	// Optional: terminate services that have been suspended for too long.
	if autoTerminateDays() > 0 {
		days := autoTerminateDays()
		_, _ = c.AddFunc("@every 1h", func() {
			withLock("scheduler:service-auto-terminate", 300*time.Second, func(ctx context.Context) {
				refs, err := st.StaleSuspendedServices(ctx, days, 200)
				if err != nil {
					log.Printf("load stale suspended services: %v", err)
					return
				}
				for _, ref := range refs {
					if err := q.ServiceTerminate(ref.PublicID); err != nil {
						log.Printf("enqueue terminate %s: %v", ref.PublicID, err)
					}
				}
			})
		})
	}
	// 试用到期回收：试用服务的 trial_ends_at 到了就终止（魔方试用商品的等价能力）。
	_, _ = c.AddFunc("@every 5m", func() {
		withLock("scheduler:trial-reclaim", 240*time.Second, func(ctx context.Context) {
			refs, err := st.ExpiredTrialServices(ctx, 200)
			if err != nil {
				log.Printf("load expired trials: %v", err)
				return
			}
			for _, ref := range refs {
				if err := q.ServiceTerminate(ref.PublicID); err != nil {
					log.Printf("enqueue trial terminate %s: %v", ref.PublicID, err)
					continue
				}
				log.Printf("trial expired, terminating service %s", ref.PublicID)
			}
		})
	})
	// Roadmap 第二十阶段: Session 清理.
	// 周期人工订单：按 num + unit 的周期为用户生成人工订单（CycleArtificialOrder
	// 插件对齐）。生成逻辑幂等：每笔订单落在对应周期时点上，重复执行不会重复生成。
	_, _ = c.AddFunc("@every 10m", func() {
		withLock("scheduler:cycle-artificial-order", 540*time.Second, func(ctx context.Context) {
			n, err := st.GenerateDueCycleArtificialOrders(ctx, 50)
			if err != nil {
				log.Printf("generate cycle artificial orders: %v", err)
				return
			}
			if n > 0 {
				log.Printf("generated %d cycle artificial order(s)", n)
			}
		})
	})
	// 客户关怀（对齐魔方 ClientCare 插件）：到点把站内信写进用户收件箱并推进任务，
	// 邮件类投递交给 mail.send 队列按指定通道发送。
	_, _ = c.AddFunc("@every 1m", func() {
		withLock("scheduler:client-care", 50*time.Second, func(ctx context.Context) {
			deliveries, err := st.RunDueClientCareJobs(ctx, 20)
			if err != nil {
				log.Printf("client care run: %v", err)
				return
			}
			for _, d := range deliveries {
				if err := q.MailSendVia(d.Email, d.Subject, d.Content, d.MailProvider); err != nil {
					log.Printf("client care mail %s: %v", d.MailPublicID, err)
				}
			}
			if len(deliveries) > 0 {
				log.Printf("client care delivered %d mail(s)", len(deliveries))
			}
		})
	})
	// 内部工单（对齐魔方 TicketInternalPremium 插件）：执行到期的定时工单，
	// 并对剩余处理时间不足 15% 的工单给处理人发站内提醒。
	_, _ = c.AddFunc("@every 1m", func() {
		withLock("scheduler:ticket-internal-cron", 50*time.Second, func(ctx context.Context) {
			n, err := st.RunDueTicketInternalCronJobs(ctx, 20)
			if err != nil {
				log.Printf("ticket internal cron: %v", err)
				return
			}
			if n > 0 {
				log.Printf("ticket internal cron created %d ticket(s)", n)
			}
		})
	})
	_, _ = c.AddFunc("@every 5m", func() {
		withLock("scheduler:ticket-internal-reminder", 4*time.Minute, func(ctx context.Context) {
			n, err := st.RunTicketInternalTimeoutReminders(ctx, 50)
			if err != nil {
				log.Printf("ticket internal reminder: %v", err)
				return
			}
			if n > 0 {
				log.Printf("ticket internal reminded %d ticket(s)", n)
			}
		})
	})
	_, _ = c.AddFunc("30 4 * * *", func() {
		withLock("scheduler:session-cleanup", 300*time.Second, func(ctx context.Context) {
			n, err := st.CleanupExpiredSessions(ctx, 7)
			if err != nil {
				log.Printf("session cleanup: %v", err)
				return
			}
			if n > 0 {
				log.Printf("purged %d dead session(s)", n)
			}
		})
	})
	c.Start()
	defer c.Stop()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
}

func autoTerminateDays() int {
	v, _ := strconv.Atoi(os.Getenv("AUTO_TERMINATE_SUSPENDED_DAYS"))
	return v
}
