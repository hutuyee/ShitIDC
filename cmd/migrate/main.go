package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/hutuyee/ShitIDC/internal/config"
	"github.com/hutuyee/ShitIDC/internal/database"
	magiccubemigration "github.com/hutuyee/ShitIDC/internal/migration/magiccube"
)

func main() {
	profilePath := flag.String("profile", "migration/magiccube/profile.example.yaml", "mapping profile")
	source := flag.String("source", "", "source MySQL DSN, e.g. readonly:pass@tcp(127.0.0.1:3306)/magiccube?parseTime=true")
	apply := flag.Bool("apply", false, "actually import; default is dry-run")
	reportPath := flag.String("report", "migration-report.json", "report output path")
	flag.Parse()
	if *source == "" {
		log.Fatal("--source is required")
	}
	profile, err := magiccubemigration.LoadProfile(*profilePath)
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	db, err := database.Open(context.Background(), cfg.PostgresDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	report, runErr := magiccubemigration.Run(context.Background(), *source, db, profile, !*apply)
	if err := magiccubemigration.SaveReport(*reportPath, report); err != nil {
		log.Printf("save report: %v", err)
	}
	fmt.Printf("users=%d/%d products=%d/%d wallets=%d/%d orders=%d/%d invoices=%d/%d payments=%d/%d services=%d/%d password_resets=%d\n", report.UsersImported, report.UsersFound, report.ProductsImported, report.ProductsFound, report.WalletsImported, report.WalletsFound, report.OrdersImported, report.OrdersFound, report.InvoicesImported, report.InvoicesFound, report.PaymentsImported, report.PaymentsFound, report.ServicesImported, report.ServicesFound, report.RequiresPasswordReset)
	if runErr != nil {
		log.Fatal(runErr)
	}
}
