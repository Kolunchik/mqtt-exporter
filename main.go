package main

import (
	"encoding/json"
	"flag"
	"log"
	"math"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	APIVersion           = "v1"
	counterSuffix        = "/count"
	counterPrefix        = "counters/"
	virtualCounterPrefix = "/devices/wb-gpio/controls/EXT"
)

type MetricData struct {
	Value     any   `json:"value"`
	Timestamp int64 `json:"ts"`
}

type Metric interface {
	GetMetric() *MetricData
	Value() any
	UpdatedAt() (*time.Time, bool)
	Hide()
	Hidden() bool
}

type stringMetric struct {
	value     string
	updatedAt time.Time
	hidden    bool
}

func (m *stringMetric) GetMetric() *MetricData {
	return &MetricData{
		Value:     m.value,
		Timestamp: m.updatedAt.UnixMilli(),
	}
}

func (m *stringMetric) Value() any {
	return m.value
}

func (m *stringMetric) UpdatedAt() (*time.Time, bool) {
	return &m.updatedAt, false
}

func (m *stringMetric) Hide() {
	m.hidden = true
}

func (m *stringMetric) Hidden() bool {
	return m.hidden
}

type floatMetric struct {
	value     float64
	updatedAt time.Time
	hidden    bool
}

func (m *floatMetric) GetMetric() *MetricData {
	return &MetricData{
		Value:     m.value,
		Timestamp: m.updatedAt.UnixMilli(),
	}
}

func (m *floatMetric) Value() any {
	return m.value
}

func (m *floatMetric) UpdatedAt() (*time.Time, bool) {
	return &m.updatedAt, false
}

func (m *floatMetric) Hide() {
	m.hidden = true
}

func (m *floatMetric) Hidden() bool {
	return m.hidden
}

type counterMetric struct {
	value     atomic.Uint64
	updatedAt time.Time
	hidden    bool
}

func (m *counterMetric) GetMetric() *MetricData {
	return &MetricData{
		Value:     m.value.Load(),
		Timestamp: m.updatedAt.UnixMilli(),
	}
}

func (m *counterMetric) Value() any {
	return m.value.Load()
}

func (m *counterMetric) UpdatedAt() (*time.Time, bool) {
	return &m.updatedAt, true
}

func (m *counterMetric) Hide() {
	m.hidden = true
}

func (m *counterMetric) Hidden() bool {
	return m.hidden
}

func (m *counterMetric) Add(v uint64) {
	m.value.Add(v)
	m.hidden = false
	m.updatedAt = time.Now()
}

var (
	metrics         sync.Map
	startTime       = time.Now()
	memStats        runtime.MemStats
	healthLock      sync.RWMutex
	countersLock    sync.Mutex
	httpRequests    atomic.Uint64
	httpRequestsOld uint64
	mqttConnections atomic.Uint64
	mqttConnected   atomic.Uint32
	mqttClient      mqtt.Client
)

var opts struct {
	broker    string
	topic     string
	maxLength int
	ttl       time.Duration
	noCleanup bool
	tiny      bool
	httpAddr  string
}

var commit = "unknown"

func parseFlags() {
	flag.StringVar(&opts.httpAddr, "http-addr", "localhost:8080", "HTTP server address")
	flag.StringVar(&opts.broker, "broker", "tcp://localhost:1883", "MQTT broker address")
	flag.StringVar(&opts.topic, "topic", "#", "MQTT topic to subscribe")
	flag.IntVar(&opts.maxLength, "max-length", 0, "Maximum payload length")
	flag.DurationVar(&opts.ttl, "ttl", 5*time.Minute, "Metrics TTL")
	flag.BoolVar(&opts.noCleanup, "no-cleanup", false, "Disable metrics cleanup")
	flag.BoolVar(&opts.tiny, "tiny", false, "noops, legacy switch")
	flag.Parse()
}

func mcStart(broker, topic string) (mqtt.Client, error) {
	mo := mqtt.NewClientOptions().AddBroker(broker)
	mo.SetClientID("mqtt-exporter")
	mo.SetAutoReconnect(true)
	mo.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		mqttConnected.Store(0)
		log.Printf("Connection lost: %v", err)
	})
	mo.SetOnConnectHandler(func(c mqtt.Client) {
		mqttConnected.Store(1)
		mqttConnections.Add(1)
		log.Printf("Connected to %v", broker)
		c.Subscribe(topic, 0, nil)
	})
	mo.SetDefaultPublishHandler(messageHandler)

	client := mqtt.NewClient(mo)

	token := client.Connect()
	token.Wait()
	return client, token.Error()
}

func main() {
	parseFlags()
	if c, err := mcStart(opts.broker, opts.topic); err != nil {
		log.Fatal("Connection error:", err)
	} else {
		mqttClient = c
	}

	go scheduledTasks(opts.noCleanup, opts.ttl)

	http.HandleFunc("/"+APIVersion+"/metrics", metricsHandler)
	http.HandleFunc("/"+APIVersion+"/health", healthHandler)
	http.HandleFunc("/"+APIVersion+"/suicide", func(w http.ResponseWriter, r *http.Request) {
		log.Fatal("kill switch on")
	})
	b := []byte("{\"name\": \"mqtt-exporter\"}")
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(b); err != nil {
			log.Printf("write error in defHandler: %s", err)
		}
	})

	log.Printf("Starting mqtt-exporter %s on %s", commit, opts.httpAddr)
	log.Fatal(http.ListenAndServe(opts.httpAddr, nil))
}

func messageHandler(_ mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	if len(topic) == 0 {
		return
	}
	payload := string(msg.Payload())

	if isCounter(topic) {
		processCounter(topic, payload)
		return
	} else if virtualCounter(topic) {
		processCounter(topic+"/vc", payload)
	}
	processRegularMetric(topic, payload, opts.maxLength)
}

func isCounter(topic string) bool {
	return strings.HasPrefix(topic, counterPrefix) || strings.HasSuffix(topic, counterSuffix)
}

func virtualCounter(topic string) bool {
	return strings.HasPrefix(topic, virtualCounterPrefix)
}

func processCounter(topic, payload string) {
	val, err := strconv.ParseUint(payload, 10, 64)
	if err != nil {
		log.Printf("Topic %v has invalid counter value: %s", topic, payload)
		return
	}

	countersLock.Lock()
	defer countersLock.Unlock()

	current, _ := metrics.LoadOrStore(topic, &counterMetric{
		updatedAt: time.Now(),
	})
	if m, ok := current.(*counterMetric); ok {
		m.Add(val)
	}
}

func processRegularMetric(topic, payload string, maxLength int) {

	if maxLength > 0 && len(payload) > maxLength {
		log.Printf("Topic %v payload length exceeds the set limit", topic)
		return
	}

	if num, err := strconv.ParseFloat(payload, 64); err != nil || math.IsNaN(num) || math.IsInf(num, 0) {
		metrics.Store(topic, &stringMetric{
			value:     payload,
			updatedAt: time.Now(),
		})
	} else {
		metrics.Store(topic, &floatMetric{
			value:     num,
			updatedAt: time.Now(),
		})
	}
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	result := map[string]*MetricData{
		"uptime_seconds":                  systemMetric(time.Since(startTime).Seconds()),
		"/devices/system/controls/uptime": systemMetric(time.Since(startTime).Seconds()),
	}

	metrics.Range(func(k, v any) bool {
		if m, ok := v.(Metric); ok && !m.Hidden() {
			result[k.(string)] = m.GetMetric()
		}
		return true
	})

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(result)

	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Printf("JSON encode error in metricsHandler: %s", err)
		return
	}
	httpRequests.Add(1)
}

func systemMetric(value any) *MetricData {
	return &MetricData{
		Value:     value,
		Timestamp: time.Now().UnixMilli(),
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	healthLock.RLock()
	defer healthLock.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(map[string]any{
		"opts": map[string]any{
			"max-length": opts.maxLength,
			"ttl":        opts.ttl.String(),
			"no-cleanup": opts.noCleanup,
			"broker":     opts.broker,
			"topic":      opts.topic,
		},
		"metrics": map[string]any{
			"mqtt_connections_total": mqttConnections.Load(),
			"mqtt_connected":         mqttConnected.Load(),
			"http_requests_total":    httpRequests.Load(),
			"memory_alloc":           memStats.Alloc,
			"memory_alloc_total":     memStats.TotalAlloc,
			"uptime":                 time.Since(startTime).String(),
		},
		"status": "ok",
		"commit": commit,
		"ts":     time.Now().UnixMilli(),
	})

	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Printf("JSON encode error in healthHandler: %s", err)
	}
}

func scheduledTasks(noCleanup bool, ttl time.Duration) {
	for range time.Tick(time.Minute) {
		cleanupTask(noCleanup, ttl)
		runtime.ReadMemStats(&memStats)
	}
}

func cleanupTask(noCleanup bool, ttl time.Duration) {
	if noCleanup {
		return
	}
	if n := httpRequests.Load(); httpRequestsOld == n {
		return
	} else {
		httpRequestsOld = n
	}
	now := time.Now()
	c := 0
	metrics.Range(func(k, v any) bool {
		if m, ok := v.(Metric); ok && !m.Hidden() {
			updatedAt, immortal := m.UpdatedAt()
			if now.Sub(*updatedAt) > ttl {
				if immortal {
					m.Hide()
					return true
				}
				metrics.Delete(k)
				c++
			}
		}
		return true
	})
	if c > 0 {
		log.Printf("%v metric(s) expired", c)
	}
}
