// Command loadtest 是一个最小 HTTP 压测器。
//
// 不引 k6/wrk：只需要固定并发 + 持续时间 + 分位数，用标准库足够。
// 它不进入生产镜像，只用于验收/容量评估。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

type result struct {
	latency time.Duration
	status  int
	err     error
}

func main() {
	url := flag.String("url", "http://127.0.0.1:8080/healthz", "target URL")
	method := flag.String("method", http.MethodGet, "HTTP method")
	body := flag.String("body", "", "request body")
	apiKey := flag.String("api-key", os.Getenv("KF_AUTH_API_KEY"), "Bearer API key")
	concurrency := flag.Int("concurrency", 20, "concurrent workers")
	duration := flag.Duration("duration", 10*time.Second, "test duration")
	timeout := flag.Duration("timeout", 10*time.Second, "per-request timeout")
	flag.Parse()

	if *concurrency < 1 || *duration <= 0 || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "concurrency/duration/timeout 必须为正数")
		os.Exit(2)
	}

	client := &http.Client{Timeout: *timeout}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	results := make(chan result, *concurrency*16)
	var wg sync.WaitGroup
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				start := time.Now()
				var reader io.Reader
				if *body != "" {
					reader = bytes.NewReader([]byte(*body))
				}
				req, err := http.NewRequestWithContext(ctx, *method, *url, reader)
				if err != nil {
					results <- result{err: err}
					return
				}
				if *body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				if *apiKey != "" {
					req.Header.Set("Authorization", "Bearer "+*apiKey)
				}
				resp, err := client.Do(req)
				latency := time.Since(start)
				if err != nil {
					// 到达 duration 后主动取消的请求不算服务端错误。
					if ctx.Err() != nil {
						return
					}
					select {
					case results <- result{latency: latency, err: err}:
					case <-ctx.Done():
					}
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				select {
				case results <- result{latency: latency, status: resp.StatusCode}:
				case <-ctx.Done():
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var (
		latencies []time.Duration
		errors    int
		statuses  = map[int]int{}
	)
	start := time.Now()
	for r := range results {
		if r.err != nil {
			errors++
			continue
		}
		latencies = append(latencies, r.latency)
		statuses[r.status]++
	}
	elapsed := time.Since(start)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	out := map[string]any{
		"url":         *url,
		"method":      *method,
		"concurrency": *concurrency,
		"duration":    elapsed.String(),
		"requests":    len(latencies) + errors,
		"ok":          len(latencies),
		"errors":      errors,
		"qps":         float64(len(latencies)+errors) / elapsed.Seconds(),
		"status":      statuses,
		"p50":         percentile(latencies, 0.50).String(),
		"p95":         percentile(latencies, 0.95).String(),
		"p99":         percentile(latencies, 0.99).String(),
	}
	raw, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(raw))
	if errors > 0 {
		os.Exit(1)
	}
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
