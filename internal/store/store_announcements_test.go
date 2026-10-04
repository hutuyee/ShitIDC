package store

import (
	"context"
	"testing"
)

// 公告详情页依赖「按 ID 取一条可见公告」：下线的公告对用户必须等于不存在，
// 否则已经撤下的内容还能被翻出来。

func TestGetAnnouncementRespectsVisibility(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	visible, err := s.CreateAnnouncement(ctx, "维护通知", "第一段\n\n第二段", true, true)
	if err != nil {
		t.Fatalf("create visible: %v", err)
	}
	// 用户能取到在线的公告。
	got, err := s.GetAnnouncement(ctx, visible.PublicID)
	if err != nil {
		t.Fatalf("get visible: %v", err)
	}
	if got.Title != "维护通知" {
		t.Fatalf("title = %q", got.Title)
	}
	if !got.Pinned {
		t.Fatal("pinned flag was lost")
	}
	// 正文里的换行必须原样保留：详情页按段落渲染，压平了就毁了排版。
	if got.Body != "第一段\n\n第二段" {
		t.Fatalf("body = %q, want the original newlines preserved", got.Body)
	}

	// 下线的公告对用户不可见。
	hidden, err := s.CreateAnnouncement(ctx, "草稿", "还没发布", false, false)
	if err != nil {
		t.Fatalf("create hidden: %v", err)
	}
	if _, err := s.GetAnnouncement(ctx, hidden.PublicID); err != ErrNotFound {
		t.Fatalf("hidden announcement = %v, want ErrNotFound", err)
	}
	// 不存在的 ID 同样是 NotFound。
	if _, err := s.GetAnnouncement(ctx, "00000000-0000-0000-0000-000000000000"); err != ErrNotFound {
		t.Fatalf("missing announcement = %v, want ErrNotFound", err)
	}

	// 列表只给在线的，并且置顶排在前面。
	if _, err := s.CreateAnnouncement(ctx, "普通公告", "x", true, false); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListAnnouncements(ctx, true, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("visible list has %d items, want 2 (the draft must be excluded)", len(list))
	}
	if !list[0].Pinned {
		t.Fatalf("pinned announcement should sort first, got %q", list[0].Title)
	}
}
