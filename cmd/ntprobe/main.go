package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/ntclient"
)

// decodePose2d parses the WPILib struct layout: x double, y double, theta double.
func decodePose2d(b []byte) (x, y, theta float64, ok bool) {
	if len(b) != 24 {
		return 0, 0, 0, false
	}
	x = math.Float64frombits(binary.LittleEndian.Uint64(b[0:8]))
	y = math.Float64frombits(binary.LittleEndian.Uint64(b[8:16]))
	theta = math.Float64frombits(binary.LittleEndian.Uint64(b[16:24]))
	return x, y, theta, true
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	c := ntclient.New("ntprobe", 4096)
	c.ConnectAddress("127.0.0.1", 5810, []string{"/"})
	defer c.Close()

	// Collect per-topic samples for 3 seconds.
	type sample struct {
		typeName string
		value    any
	}
	var (
		mu      sync.Mutex
		samples = map[string][]sample{}
	)

	ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case v, ok := <-c.Values():
				if !ok {
					return
				}
				if !strings.HasPrefix(v.Type, "struct:") {
					continue
				}
				mu.Lock()
				samples[v.Name] = append(samples[v.Name], sample{v.Type, v.Value})
				mu.Unlock()
			}
		}
	}()
	<-done

	mu.Lock()
	defer mu.Unlock()

	fmt.Println("=== Struct topic samples (3s window) ===")
	names := make([]string, 0, len(samples))
	for n := range samples {
		names = append(names, n)
	}
	// Stable ordering
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}

	for _, n := range names {
		ss := samples[n]
		first := ss[0]
		fmt.Printf("\n%s\n", n)
		fmt.Printf("  type=%s updates=%d\n", first.typeName, len(ss))
		b, ok := first.value.([]byte)
		if !ok {
			fmt.Printf("  value_go_type=%T (unexpected!)\n", first.value)
			continue
		}
		if first.typeName == "struct:Pose2d" {
			x, y, theta, ok := decodePose2d(b)
			if ok {
				fmt.Printf("  first   : x=%+8.4f y=%+8.4f θ=%+8.4f rad\n", x, y, theta)
			}
			if len(ss) > 1 {
				lb := ss[len(ss)-1].value.([]byte)
				lx, ly, lt, _ := decodePose2d(lb)
				fmt.Printf("  last    : x=%+8.4f y=%+8.4f θ=%+8.4f rad\n", lx, ly, lt)
			}
		} else if first.typeName == "struct:Pose2d[]" {
			fmt.Printf("  payload_bytes=%d → %d poses\n", len(b), len(b)/24)
		}
	}
}
