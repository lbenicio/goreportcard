package storage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type fakeRESPServer struct {
	listener net.Listener
	mu       sync.Mutex
	values   map[string]string
	pingErr  bool
}

func startFakeRESPServer(t *testing.T, pingErr bool) *fakeRESPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeRESPServer{listener: listener, values: make(map[string]string), pingErr: pingErr}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.serve(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	return server
}

func (s *fakeRESPServer) url() string {
	return "redis://" + s.listener.Addr().String() + "/0"
}

func (s *fakeRESPServer) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		command, err := readRESPCommand(reader)
		if err != nil {
			return
		}
		if len(command) == 0 {
			_, _ = conn.Write([]byte("-ERR empty command\r\n"))
			continue
		}
		var response string
		s.mu.Lock()
		switch strings.ToUpper(command[0]) {
		case "PING":
			if s.pingErr {
				response = "-ERR simulated ping failure\r\n"
			} else {
				response = "+PONG\r\n"
			}
		case "HELLO":
			response = "-ERR unknown command 'HELLO'\r\n"
		case "GET":
			if value, ok := s.values[command[1]]; ok {
				response = fmt.Sprintf("$%d\r\n%s\r\n", len(value), value)
			} else {
				response = "$-1\r\n"
			}
		case "SET":
			s.values[command[1]] = command[2]
			response = "+OK\r\n"
		case "DEL":
			count := 0
			if _, ok := s.values[command[1]]; ok {
				delete(s.values, command[1])
				count = 1
			}
			response = fmt.Sprintf(":%d\r\n", count)
		default:
			// go-redis sends CLIENT SETINFO and other optional setup commands.
			response = "+OK\r\n"
		}
		s.mu.Unlock()
		if _, err := conn.Write([]byte(response)); err != nil {
			return
		}
	}
}

func readRESPCommand(reader *bufio.Reader) ([]string, error) {
	line, err := readRESPLine(reader)
	if err != nil {
		return nil, err
	}
	if len(line) < 2 || line[0] != '*' {
		return nil, fmt.Errorf("expected RESP array, got %q", line)
	}
	count, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, err
	}
	command := make([]string, count)
	for i := range command {
		line, err = readRESPLine(reader)
		if err != nil {
			return nil, err
		}
		if len(line) < 2 || line[0] != '$' {
			return nil, fmt.Errorf("expected RESP bulk string, got %q", line)
		}
		length, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		if length < 0 {
			command[i] = ""
			continue
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return nil, err
		}
		command[i] = string(value[:length])
	}
	return command, nil
}

func readRESPLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func TestOpenRedisCacheValidatesURL(t *testing.T) {
	if _, err := OpenRedisCache(context.Background(), &memoryReportStore{}, " \t"); err == nil || !strings.Contains(err.Error(), "REDIS_URL is required") {
		t.Fatalf("OpenRedisCache(empty URL) error = %v", err)
	}
	if _, err := OpenRedisCache(context.Background(), &memoryReportStore{}, "not a redis URL"); err == nil || !strings.Contains(err.Error(), "parse REDIS_URL") {
		t.Fatalf("OpenRedisCache(invalid URL) error = %v", err)
	}
}

func TestOpenRedisCacheAndRedisReportCacheOperations(t *testing.T) {
	server := startFakeRESPServer(t, false)
	store, err := OpenRedisCache(context.Background(), &memoryReportStore{}, "  "+server.url()+"  ")
	if err != nil {
		t.Fatal(err)
	}
	cache := store.cache.(*redisReportCache)
	ctx := context.Background()

	if _, err := cache.Get(ctx, "report"); !errors.Is(err, errReportCacheMiss) {
		t.Fatalf("Get(missing key) error = %v, want cache miss", err)
	}
	if err := cache.Set(ctx, "report", "encoded report", time.Minute); err != nil {
		t.Fatal(err)
	}
	value, err := cache.Get(ctx, "report")
	if err != nil || value != "encoded report" {
		t.Fatalf("Get(set key) = (%q, %v)", value, err)
	}
	if err := cache.Delete(ctx, "report"); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(ctx, "report"); !errors.Is(err, errReportCacheMiss) {
		t.Fatalf("Get(deleted key) error = %v, want cache miss", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOpenRedisCacheTreatsPingFailureAsBestEffort(t *testing.T) {
	server := startFakeRESPServer(t, true)
	store, err := OpenRedisCache(context.Background(), &memoryReportStore{}, server.url())
	if err != nil {
		t.Fatalf("OpenRedisCache() error = %v, want best-effort startup", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestRedisStoreReturnsReportWhenJSONEncodingFails(t *testing.T) {
	want := Report{
		Repo:        "github.com/example/future",
		LastRefresh: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
	cache := &memoryReportCache{values: make(map[string]string)}
	base := &memoryReportStore{report: want}
	store := &RedisStore{Store: base, cache: cache}

	got, err := store.GetReport(context.Background(), want.Repo)
	if err != nil {
		t.Fatalf("GetReport() error = %v, want cache encoding failure to be best-effort", err)
	}
	if got.Repo != want.Repo || !got.LastRefresh.Equal(want.LastRefresh) {
		t.Fatalf("GetReport() = %+v, want report %+v", got, want)
	}
	if cache.setCalls != 0 {
		t.Fatalf("Redis Set called %d times, want 0 for an unencodable report", cache.setCalls)
	}
}

func TestRedisReportCacheReturnsRedisErrors(t *testing.T) {
	server := startFakeRESPServer(t, false)
	options, err := redis.ParseURL(server.url())
	if err != nil {
		t.Fatal(err)
	}
	cache := &redisReportCache{client: redis.NewClient(options)}
	defer cache.Close()

	server.mu.Lock()
	server.values["malformed"] = "one\r\ntwo"
	server.mu.Unlock()
	value, err := cache.Get(context.Background(), "malformed")
	if err != nil || value != "one\r\ntwo" {
		t.Fatalf("Get(malformed payload) = (%q, %v)", value, err)
	}
}
