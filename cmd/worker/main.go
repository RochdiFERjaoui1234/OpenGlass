package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/openglass/openglass/internal/queue"
	"github.com/openglass/openglass/internal/worker"
)

func main() {
	url := os.Getenv("NATS_URL")
	bus, err := queue.Connect(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer bus.Close()
	if err := bus.OnJob(worker.Execute); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("openglass worker listening for jobs")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}
