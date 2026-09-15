package main

import (
	"embed"
	"log"
	"net/http"
	"os"

	"github.com/jhopan/agenpulsa-server/internal/api"
	"github.com/jhopan/agenpulsa-server/internal/db"
	"github.com/jhopan/agenpulsa-server/internal/engine"
	"github.com/jhopan/agenpulsa-server/internal/scheduler"
)

//go:embed web
var webFS embed.FS

func main() {
	port := os.Getenv("AP_PORT")
	if port == "" {
		port = "8081"
	}
	dbPath := os.Getenv("AP_DB")
	if dbPath == "" {
		dbPath = "data/agenpulsa.db"
	}

	store, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer store.Close()

	eng, err := engine.New(store)
	if err != nil {
		log.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	// Satu worker eksekusi order dari queue (urut, satu browser).
	go eng.Worker()

	// Scheduler jadwal harian/interval/sekali (WIB) + pengecekan saldo.
	sched := scheduler.New(store, eng)
	go sched.Run()

	h := api.New(store, eng, webFS)
	h.StartReconcile() // poll paypan: order paid/expired gak nyangkut walau webhook hilang
	h.StartCookieReminder() // notif inject cookies baru tiap 3 hari (log + ntfy opsional)
	log.Printf("agenpulsa-server jalan di :%s (WIB %s)", port, sched.NowWIB().Format("2006-01-02 15:04"))
	log.Fatal(http.ListenAndServe(":"+port, h.Routes()))
}
