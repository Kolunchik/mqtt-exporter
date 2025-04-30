package main

import (
	"encoding/base64"
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

	//"unicode"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	APIVersion    = "v1"
	counterSuffix = "/count"
	counterPrefix = "counters/"
)

type MetricData struct {
	Topic     string `json:"topic,omitempty"`
	Type      string `json:"type,omitempty"`
	Value     any    `json:"value,omitempty"`
	Binary    string `json:"binary,omitempty"`
	Timestamp int64  `json:"ts"`
	RFC3339   string `json:"rfc3339,omitempty"`
}

type metricValue struct {
	valueType string
	number    float64
	text      string
	binary    []byte
	counter   uint64
	updatedAt time.Time
}

var (
	metrics         sync.Map
	startTime       = time.Now()
	memStats        runtime.MemStats
	metricsLock     sync.RWMutex
	countersLock    sync.Mutex
	httpRequests    atomic.Uint64
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
	flag.BoolVar(&opts.tiny, "tiny", false, "Compact output")
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

	if !opts.noCleanup {
		go scheduledCleanupTask(opts.ttl)
	}
	go scheduledCollectMemoryStats()

	http.HandleFunc("/"+APIVersion+"/metrics", metricsHandler)
	http.HandleFunc("/"+APIVersion+"/health", healthHandler)
	http.HandleFunc("/"+APIVersion+"/suicide", func(w http.ResponseWriter, r *http.Request) {
		log.Fatal("kill switch on")
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(map[string]string{
			"name":   "mqtt-exporter",
			"commit": commit,
		})
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			log.Printf("JSON encode error: %s", err)
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
	payload := msg.Payload()

	if isCounter(topic) {
		processCounter(topic, payload)
	} else {
		processRegularMetric(topic, payload, opts.maxLength)
	}
}

func isCounter(topic string) bool {
	return strings.HasPrefix(topic, counterPrefix) || strings.HasSuffix(topic, counterSuffix)
}

func processCounter(topic string, payload []byte) {
	val, err := strconv.ParseFloat(string(payload), 64)
	if err != nil {
		log.Printf("Topic %v has invalid counter value: %s", topic, payload)
		return
	}

	countersLock.Lock()
	defer countersLock.Unlock()

	if current, loaded := metrics.LoadOrStore(topic, &metricValue{
		valueType: "counter",
		counter:   uint64(val),
		updatedAt: time.Now(),
	}); loaded {
		m := current.(*metricValue)
		atomic.AddUint64(&m.counter, uint64(val))
		m.updatedAt = time.Now()
	}
}

func processRegularMetric(topic string, payload []byte, maxLength int) {
	var value any
	var valueType string

	if maxLength > 0 && len(payload) > maxLength {
		log.Printf("Topic %v payload length exceeds the set limit", topic)
		return
	}

	if num, err := strconv.ParseFloat(string(payload), 64); err == nil {
		if math.IsNaN(num) {
			value = string(payload)
			valueType = "text"
		} else if math.IsInf(num, 0) {
			value = string(payload)
			valueType = "text"
		} else {
			value = num
			valueType = "number"
		}
	} else if isBinaryData(payload) {
		value = payload
		valueType = "binary"
	} else {
		value = string(payload)
		valueType = "text"
	}

	metricVal := &metricValue{
		valueType: valueType,
		updatedAt: time.Now(),
	}

	switch v := value.(type) {
	case float64:
		metricVal.number = v
	case string:
		metricVal.text = v
	case []byte:
		metricVal.binary = v
	default:
		log.Printf("Topic %v has unexpected value type: %T", topic, v)
		return
	}

	metrics.Store(topic, metricVal)
}

func isBinaryData(_ []byte) bool {
	/*
		const maxTextCheck = 512
		checkLength := len(data)

		if checkLength < 1 {
			return false
		}

		if data[0] == '{' {
			return false
		}

		if checkLength > maxTextCheck {
			checkLength = maxTextCheck
		}

		for _, b := range data[:checkLength] {
			if !unicode.Is(unicode.Cyrillic, rune(b)) {
				return false
			}
			if !unicode.IsPrint(rune(b)) && !unicode.IsSpace(rune(b)) {
				return true
			}
		}
	*/
	return false
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	httpRequests.Add(1)
	result := map[string]MetricData{
		"uptime_seconds": systemMetric("uptime_seconds", "counter", time.Since(startTime).Seconds()),
	}

	metrics.Range(func(k, v any) bool {
		key := k.(string)
		m := v.(*metricValue)

		ts := m.updatedAt.UnixNano() / 1e6
		metric := MetricData{
			Timestamp: ts,
		}

		if !opts.tiny {
			metric.Topic = key
			metric.Type = m.valueType
			metric.RFC3339 = time.Unix(0, ts*int64(time.Millisecond)).Format(time.RFC3339)
		}

		switch m.valueType {
		case "counter":
			metric.Value = atomic.LoadUint64(&m.counter)
		case "number":
			metric.Value = m.number
		case "text":
			metric.Value = m.text
		case "binary":
			metric.Binary = base64.StdEncoding.EncodeToString(m.binary)
		default:
			return true
		}

		result[key] = metric
		return true
	})

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(result)

	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Printf("JSON encode error in metricsHandler: %s", err)
	}
}

func getMetricValue(m *metricValue) any {
	switch m.valueType {
	case "counter":
		return atomic.LoadUint64(&m.counter)
	case "number":
		return m.number
	case "text":
		return m.text
	case "binary":
		return base64.StdEncoding.EncodeToString(m.binary)
	default:
		log.Printf("Metric has unexpected value type: %v", m.valueType)
		return nil
	}
}

func systemMetric(topic, metricType string, value float64) MetricData {
	now := time.Now()
	return MetricData{
		Topic:     topic,
		Type:      metricType,
		Value:     value,
		Timestamp: now.UnixNano() / 1e6,
		RFC3339:   now.Format(time.RFC3339),
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	metricsLock.RLock()
	defer metricsLock.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(map[string]any{
		"opts": map[string]any{
			"max-length": opts.maxLength,
			"tiny":       opts.tiny,
			"ttl":        opts.ttl.String(),
			"no-cleanup": opts.noCleanup,
			"broker":     opts.broker,
			"topic":      opts.topic,
		},
		"metrics": map[string]any{
			"mqtt_connections_total": float64(mqttConnections.Load()),
			"mqtt_connected":         float64(mqttConnected.Load()),
			"http_requests_total":    float64(httpRequests.Load()),
			"memory_alloc":           float64(memStats.Alloc),
			"memory_total_alloc":     float64(memStats.TotalAlloc),
			"uptime":                 time.Since(startTime).String(),
		},
		"status":    "ok",
		"commit":    commit,
		"timestamp": time.Now().Unix(),
	})

	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Printf("JSON encode error in healthHandler: %s", err)
	}
}

func scheduledCleanupTask(ttl time.Duration) {
	for range time.Tick(time.Minute) {
		cleanupTask(false, ttl)
	}
}

func cleanupTask(noCleanup bool, ttl time.Duration) {
	if noCleanup {
		return
	}
	log.Printf("Starting cleanupTask")
	now := time.Now()
	c := 0
	metrics.Range(func(k, v any) bool {
		m := v.(*metricValue)
		if now.Sub(m.updatedAt) > ttl {
			metrics.Delete(k)
			c++
		}
		return true
	})
	log.Printf("%v metric(s) expired", c)
}

func scheduledCollectMemoryStats() {
	for range time.Tick(5 * time.Second) {
		collectMemoryStats()
	}
}

func collectMemoryStats() {
	runtime.ReadMemStats(&memStats)
}
