package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"film-fusion/app/database"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestShutdownKeepsDatabaseUntilRequestsDrain(t *testing.T) {
	for _, expire := range []bool{false, true} {
		name := "drain"
		if expire {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "shutdown.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			pool, _ := db.DB()
			defer pool.Close()
			previous := database.DB
			database.DB = db
			defer func() { database.DB = previous }()
			entered, release, requestDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
				requestDone <- db.Exec("SELECT 1").Error
				w.WriteHeader(http.StatusNoContent)
			})}
			defer httpServer.Close()
			go httpServer.Serve(listener)
			clientDone := make(chan struct{})
			go func() {
				defer close(clientDone)
				resp, e := http.Get("http://" + listener.Addr().String())
				if e == nil {
					resp.Body.Close()
				}
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("request never entered")
			}
			s := &Server{http: httpServer, streamShutdown: make(chan struct{})}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if expire {
				cancel()
			}
			shutdownDone := make(chan error, 1)
			go func() { shutdownDone <- s.Shutdown(ctx) }()
			if expire {
				if err := <-shutdownDone; !errors.Is(err, context.Canceled) {
					t.Fatalf("shutdown error=%v", err)
				}
				if err := pool.Ping(); err != nil {
					t.Fatalf("database closed with active request: %v", err)
				}
			} else {
				select {
				case err := <-shutdownDone:
					t.Fatalf("shutdown returned early: %v", err)
				case <-time.After(50 * time.Millisecond):
				}
			}
			close(release)
			if err := <-requestDone; err != nil {
				t.Fatalf("in-flight request lost DB: %v", err)
			}
			<-clientDone
			if !expire {
				if err := <-shutdownDone; err != nil {
					t.Fatal(err)
				}
				if err := pool.Ping(); err == nil {
					t.Fatal("database left open after successful shutdown")
				}
			}
		})
	}
}
