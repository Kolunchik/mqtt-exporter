package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	APIVersion    = "v1"
	counterSuffix = "/count"
	counterPrefix = "counters/"
)

type MetricData struct {
	Topic     string      `json:"topic,omitempty"`
	Type      string      `json:"type,omitempty"`
	Value     interface{} `json:"value,omitempty"`
	Binary    string      `json:"binary,omitempty"`
	Timestamp int64       `json:"ts"`
	RFC3339   string      `json:"rfc3339,omitempty"`
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
	ttl             time.Duration
	startTime       = time.Now()
	appVersion      = "1.0.0"
	memStats        runtime.MemStats
	metricsLock     sync.RWMutex
	countersLock    sync.Mutex
	httpRequests    atomic.Uint64
	mqttConnections atomic.Uint64
	noCleanup       bool
	tiny            bool
	mqttConnected   atomic.Uint32
	maxLength       int
	mqttBroker      string
	mqttTopic       string
)

func main() {
	httpAddr := flag.String("http-addr", "localhost:8080", "HTTP server address")
	flag.StringVar(&mqttBroker, "mqtt-broker", "tcp://localhost:1883", "MQTT broker address")
	flag.StringVar(&mqttTopic, "mqtt-topic", "#", "MQTT topic to subscribe")
	flag.IntVar(&maxLength, "max-length", 0, "Maximum payload length")
	flag.DurationVar(&ttl, "ttl", 5*time.Minute, "Metrics TTL")
	flag.BoolVar(&noCleanup, "no-cleanup", false, "Disable metrics cleanup")
	flag.BoolVar(&tiny, "tiny", false, "Compact output")
	flag.Parse()

	opts := mqtt.NewClientOptions().AddBroker(mqttBroker)
	opts.SetClientID("mqtt-exporter")
	opts.SetAutoReconnect(true)
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		mqttConnected.Store(0)
		log.Printf("Connection lost: %v", err)
	})
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		mqttConnected.Store(1)
		mqttConnections.Add(1)
		log.Printf("Connected to %v", mqttBroker)
		c.Subscribe(mqttTopic, 0, nil)
	})
	opts.SetDefaultPublishHandler(messageHandler)

	client := mqtt.NewClient(opts)

	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatal("Connection error:", token.Error())
	}

	if !noCleanup {
		go scheduledCleanupTask()
	}
	go scheduledCollectMemoryStats()

	http.HandleFunc("/"+APIVersion+"/metrics", metricsHandler)
	http.HandleFunc("/"+APIVersion+"/health", healthHandler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"name":    "MQTT Exporter",
			"version": appVersion,
			"api":     APIVersion,
		})
	})

	log.Printf("Starting mqtt-exporter v%s on %s", appVersion, *httpAddr)
	log.Fatal(http.ListenAndServe(*httpAddr, nil))
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
		processRegularMetric(topic, payload)
	}
}

func isCounter(topic string) bool {
	return strings.HasPrefix(topic, counterPrefix) || strings.HasSuffix(topic, counterSuffix)
}

func processCounter(topic string, payload []byte) {
	val, err := strconv.ParseFloat(string(payload), 64)
	if err != nil {
		log.Printf("Invalid counter value: %s", payload)
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

func processRegularMetric(topic string, payload []byte) {
	var value interface{}
	var valueType string

	if maxLength > 0 && len(payload) > maxLength {
		return
	}

	if num, err := strconv.ParseFloat(string(payload), 64); err == nil {
		if num != num {
			value = "NaN"
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
		log.Printf("Unexpected value type: %T", v)
		return
	}

	metrics.Store(topic, metricVal)
}

func isBinaryData(data []byte) bool {
	return false

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
	return false
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	httpRequests.Add(1)
	now := time.Now()
	result := make(map[string]MetricData)

	result["timestamp"] = systemMetric("timestamp", "number", float64(now.Unix()))

	metrics.Range(func(k, v interface{}) bool {
		key := k.(string)
		m := v.(*metricValue)

		ts := m.updatedAt.UnixNano() / 1e6
		metric := MetricData{
			Timestamp: ts,
		}

		if !tiny {
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
		log.Printf("JSON encode error: %s", err)
	}
}

func getMetricValue(m *metricValue) interface{} {
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

	result := []MetricData{
		systemMetric("mqtt_connections_total", "counter", float64(mqttConnections.Load())),
		systemMetric("mqtt_connected", "gauge", float64(mqttConnected.Load())),
		systemMetric("http_requests_total", "counter", float64(httpRequests.Load())),
		systemMetric("memory_alloc", "gauge", float64(memStats.Alloc)),
		systemMetric("memory_total_alloc", "counter", float64(memStats.TotalAlloc)),
		systemMetric("uptime_seconds", "counter", time.Since(startTime).Seconds()),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"maxLength":   maxLength,
		"tiny":        tiny,
		"ttl":         ttl.String(),
		"noCleanup":   noCleanup,
		"uptime":      time.Since(startTime).String(),
		"version":     appVersion,
		"api_version": APIVersion,
		"timestamp":   time.Now().Unix(),
		"metrics":     result,
		"mqttBroker":  mqttBroker,
		"mqttTopic":   mqttTopic,
	})
}

func scheduledCleanupTask() {
	for range time.Tick(time.Minute) {
		cleanupTask()
	}

}

func cleanupTask() {
	if noCleanup {
		return
	}

	log.Printf("Starting cleanupTask")
	now := time.Now()
	c := 0
	metrics.Range(func(k, v interface{}) bool {
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
