package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var httpRequestsCounter atomic.Uint64

type mockMessage struct {
	topic   string
	payload []byte
}

func (m *mockMessage) Duplicate() bool {
	return false
}

func (m *mockMessage) Qos() byte {
	return 0
}

func (m *mockMessage) Retained() bool {
	return false
}

func (m *mockMessage) Topic() string {
	return m.topic
}

func (m *mockMessage) MessageID() uint16 {
	return 0
}

func (m *mockMessage) Payload() []byte {
	return m.payload
}

func (m *mockMessage) Ack() {
}

func TestIsCounter(t *testing.T) {
	tests := []struct {
		name     string
		topic    string
		expected bool
	}{
		{"Empty topic", "", false},
		{"Valid counter prefix", "counters/test", true},
		{"Valid counter suffix", "test/count", true},
		{"Invalid topic", "test/topic", false},
		{"Partial prefix", "counter/test", false},
		{"Partial suffix", "test/counter", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isCounter(tt.topic))
		})
	}
}

func TestProcessCounter_InvalidPayload(t *testing.T) {
	topic := "counters/test"
	invalidPayload := "not_a_number"

	processCounter(topic, invalidPayload)

	_, loaded := metrics.Load(topic)
	assert.False(t, loaded, "Метрика не должна быть загружена при невалидном payload")
}

func TestProcessCounter(t *testing.T) {
	topic := "counters/test"
	payload := "10"

	processCounter(topic, payload)

	val, loaded := metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена")

	m := val.(*metricValue)
	assert.Equal(t, uint64(10), atomic.LoadUint64(&m.counter), "Значение счетчика должно быть 10")

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			processCounter(topic, payload)
		}()
	}
	wg.Wait()

	processCounter(topic, payload)

	val, loaded = metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена")

	m = val.(*metricValue)
	assert.Equal(t, uint64(1020), atomic.LoadUint64(&m.counter), "Значение счетчика должно быть 1020 (0x3fc)")
}

func TestProcessRegularMetric(t *testing.T) {
	topic := "test/topic"
	payload := "42.5"

	processRegularMetric(topic, payload, 0)

	val, loaded := metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена")

	m := val.(*metricValue)
	assert.Equal(t, 42.5, m.number, "Значение метрики должно быть 42.5")

	topic = "test/topic/NaN"
	payload = "NaN"

	processRegularMetric(topic, payload, 0)

	val, loaded = metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена")

	m = val.(*metricValue)
	assert.Equal(t, "NaN", m.text, "Значение метрики должно быть NaN")
	assert.Equal(t, "text", m.valueType, "Тип метрики должно быть text")

	topic = "test/topic/+Inf"
	payload = "+Inf"

	processRegularMetric(topic, payload, 0)

	val, loaded = metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена")

	m = val.(*metricValue)
	assert.Equal(t, "+Inf", m.text, "Значение метрики должно быть +Inf")
	assert.Equal(t, "text", m.valueType, "Тип метрики должно быть text")
}

func TestProcessRegularMetricMaxLength(t *testing.T) {
	maxLength := 0
	topic := "test/short"
	payload := "blah"

	processRegularMetric(topic, payload, maxLength)

	_, loaded := metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена")

	maxLength = 2

	processRegularMetric(topic, payload, maxLength)

	topic = "test/long"
	payload = "blah"

	_, loaded = metrics.Load(topic)
	assert.False(t, loaded, "Метрика не должна быть загружена")

	maxLength = 0
}

func TestProcessRegularMetric_InvalidPayload(t *testing.T) {
	topic := "test/topic"
	invalidPayload := "not_a_number"

	processRegularMetric(topic, invalidPayload, 0)

	val, loaded := metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена даже при невалидном payload")

	m := val.(*metricValue)
	assert.Equal(t, "text", m.valueType, "Тип метрики должен быть 'text' при невалидном payload")
	assert.Equal(t, "not_a_number", m.text, "Текст метрики должен соответствовать payload")
}

func TestMetricsHandler(t *testing.T) {
	metrics.Clear()

	metrics.Store("test/topic", &metricValue{
		valueType: "number",
		number:    42.5,
		updatedAt: time.Now(),
	})
	metrics.Store("counters/test/text", &metricValue{
		valueType: "text",
		text:      "test text",
		updatedAt: time.Now(),
	})
	metrics.Store("counters/test", &metricValue{
		valueType: "counter",
		counter:   10,
		updatedAt: time.Now(),
	})
	metrics.Store("counters/test/unc", &metricValue{
		valueType: "unk",
		updatedAt: time.Now(),
	})

	req, err := http.NewRequest("GET", "/v1/metrics", nil)
	assert.NoError(t, err, "Ошибка создания запроса")

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(metricsHandler)

	handler.ServeHTTP(rr, req)
	httpRequestsCounter.Add(1)

	assert.Equal(t, http.StatusOK, rr.Code, "Код ответа должен быть 200")

	var result map[string]MetricData
	err = json.Unmarshal(rr.Body.Bytes(), &result)
	assert.NoError(t, err, "Ошибка декодирования JSON")

	_, ok := result["counters/test/unc"]
	assert.False(t, ok, "Ответ НЕ должен содержать метрику counter/test/unc")

	assert.Contains(t, result, "uptime_seconds", "Ответ должен содержать метрику uptime_seconds")

	timestampMetric := result["uptime_seconds"]
	assert.Equal(t, "counter", timestampMetric.Type, "Тип системной метрики должен быть 'counter'")
	assert.NotZero(t, timestampMetric.Timestamp, "Временная метка должна быть не нулевой")

	testTopicMetric, ok := result["test/topic"]
	assert.True(t, ok, "Метрика 'test/topic' должна присутствовать в ответе")
	assert.Equal(t, "number", testTopicMetric.Type, "Тип метрики должен быть 'number'")
	assert.Equal(t, 42.5, testTopicMetric.Value, "Значение метрики должно быть 42.5")
	assert.Contains(t, testTopicMetric.RFC3339, "T", "Поле RFC3339 метрики должно содержать T")

	counterMetric, ok := result["counters/test"]
	assert.True(t, ok, "Метрика 'counters/test' должна присутствовать в ответе")
	assert.Equal(t, "counter", counterMetric.Type, "Тип метрики должен быть 'counter'")
	assert.Equal(t, float64(10), counterMetric.Value, "Значение счетчика должно быть 10")
	assert.Contains(t, counterMetric.RFC3339, "T", "Поле RFC3339 метрики должно содержать T")

	textMetric, ok := result["counters/test/text"]
	assert.True(t, ok, "Метрика 'counters/test/text' должна присутствовать в ответе")
	assert.Equal(t, "text", textMetric.Type, "Тип метрики должен быть 'text'")
	assert.Contains(t, textMetric.RFC3339, "T", "Поле RFC3339 метрики должно содержать T")
}

func TestMetricsHandlerTiny(t *testing.T) {
	metrics.Clear()

	now := time.Now()

	metrics.Store("test/topic", &metricValue{
		valueType: "number",
		number:    42.5,
		updatedAt: now,
	})
	metrics.Store("counters/test/text", &metricValue{
		valueType: "text",
		text:      "test text",
		updatedAt: time.Now(),
	})
	metrics.Store("counters/test", &metricValue{
		valueType: "counter",
		counter:   10,
		updatedAt: now,
	})

	opts.tiny = true

	loops := 100
	var wg sync.WaitGroup
	for range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest("GET", "/v1/metrics", nil)
			assert.NoError(t, err, "Ошибка создания запроса")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(metricsHandler)

			handler.ServeHTTP(rr, req)
			httpRequestsCounter.Add(1)

			assert.Equal(t, http.StatusOK, rr.Code, "Код ответа должен быть 200")

			var result map[string]MetricData
			err = json.Unmarshal(rr.Body.Bytes(), &result)
			assert.NoError(t, err, "Ошибка декодирования JSON")

			assert.Contains(t, result, "uptime_seconds", "Ответ должен содержать метрику uptime_seconds")

			timestampMetric := result["uptime_seconds"]
			assert.Equal(t, "counter", timestampMetric.Type, "Тип системной метрики должен быть 'counter'")
			assert.NotZero(t, timestampMetric.Timestamp, "Временная метка должна быть не нулевой")

			testTopicMetric, ok := result["test/topic"]
			assert.True(t, ok, "Метрика 'test/topic' должна присутствовать в ответе")
			assert.NotContains(t, testTopicMetric.Type, "number", "Тип метрики должен отсутствовать")
			assert.Equal(t, "", testTopicMetric.RFC3339, "Поле RFC3339 должно быть пустым")
			assert.Equal(t, 42.5, testTopicMetric.Value, "Значение метрики должно быть 42.5")

			textMetric, ok := result["counters/test/text"]
			assert.True(t, ok, "Метрика 'counters/test/text' должна присутствовать в ответе")
			assert.NotContains(t, testTopicMetric.Type, "text", "Тип метрики должен отсутствовать")
			assert.Equal(t, "", textMetric.RFC3339, "Поле RFC3339 должно быть пустым")

			counterMetric, ok := result["counters/test"]
			assert.True(t, ok, "Метрика 'counters/test' должна присутствовать в ответе")
			assert.NotContains(t, counterMetric.Type, "counter", "Тип метрики должен отсутстовать")
			assert.Equal(t, "", counterMetric.RFC3339, "Поле RFC3339 должно быть пустым")
			assert.Equal(t, float64(10), counterMetric.Value, "Значение счетчика должно быть 10")
		}()
	}
	wg.Wait()
	assert.Equal(t, httpRequests.Load(), httpRequestsCounter.Load())
}

func TestMetricsHandler_EmptyMetrics(t *testing.T) {
	req, err := http.NewRequest("GET", "/v1/metrics", nil)
	assert.NoError(t, err, "Ошибка создания запроса")

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(metricsHandler)

	metrics.Clear()

	handler.ServeHTTP(rr, req)
	httpRequestsCounter.Add(1)

	assert.Equal(t, http.StatusOK, rr.Code, "Код ответа должен быть 200")

	var result map[string]MetricData
	err = json.Unmarshal(rr.Body.Bytes(), &result)
	assert.NoError(t, err, "Ошибка декодирования JSON")

	assert.Len(t, result, 1, "Ответ должен содержать одну метрикy")
	assert.Contains(t, result, "uptime_seconds", "Ответ должен содержать метрику uptime_seconds")
}

func TestHealthHandler(t *testing.T) {
	var wg sync.WaitGroup
	loop := 5
	for range loop {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest("GET", "/v1/health", nil)
			assert.NoError(t, err, "Ошибка создания запроса")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(healthHandler)

			handler.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code, "Код ответа должен быть 200")

			var result map[string]any
			err = json.Unmarshal(rr.Body.Bytes(), &result)
			assert.NoError(t, err, "Ошибка декодирования JSON")

			assert.Equal(t, "ok", result["status"], "Статус должен быть 'ok'")
			assert.Contains(t, result, "metrics", "Ответ должен содержать служебные метрики")

			var m map[string]any
			m = result["metrics"].(map[string]any)
			assert.Equal(t, httpRequestsCounter.Load(), uint64(m["http_requests_total"].(float64)))
			assert.Equal(t, mqttConnections.Load(), uint64(m["mqtt_connections_total"].(float64)))
			assert.Equal(t, mqttConnected.Load(), uint32(m["mqtt_connected"].(float64)))
		}()
	}
	wg.Wait()
}

func TestHealthHandler_NoMetrics(t *testing.T) {
	req, err := http.NewRequest("GET", "/v1/health", nil)
	assert.NoError(t, err, "Ошибка создания запроса")

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(healthHandler)

	metrics.Clear()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code, "Код ответа должен быть 200")

	var result map[string]any
	err = json.Unmarshal(rr.Body.Bytes(), &result)
	assert.NoError(t, err, "Ошибка декодирования JSON")

	assert.Equal(t, "ok", result["status"], "Статус должен быть 'ok'")
	assert.Equal(t, commit, result["commit"])
	assert.Contains(t, result, "metrics", "Ответ должен содержать служебные метрики")

	var m map[string]any
	m = result["metrics"].(map[string]any)
	assert.Equal(t, httpRequestsCounter.Load(), uint64(m["http_requests_total"].(float64)))
	assert.Equal(t, mqttConnections.Load(), uint64(m["mqtt_connections_total"].(float64)))
	assert.Equal(t, mqttConnected.Load(), uint32(m["mqtt_connected"].(float64)))
}

func TestMessageHandler(t *testing.T) {

	topic := "test/topic"
	message := &mockMessage{
		topic:   topic,
		payload: []byte("42.5"),
	}

	topic1 := "test/topic/count"
	message1 := &mockMessage{
		topic:   topic1,
		payload: []byte("101"),
	}

	var wg sync.WaitGroup
	loops := 100
	for range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			messageHandler(nil, message)
			messageHandler(nil, message1)
		}()
	}
	wg.Wait()

	val, loaded := metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена")
	m := val.(*metricValue)
	assert.Equal(t, 42.5, m.number, "Значение метрики должно быть 42.5")

	val1, loaded := metrics.Load(topic1)
	m1 := val1.(*metricValue)
	assert.True(t, loaded, "Метрика должна быть загружена")
	assert.Equal(t, uint64(101*loops), m1.counter, "Значение метрики должно быть %v", 101*loops)
}

func TestMessageHandler_InvalidTopic(t *testing.T) {
	topic := "" // Пустой топик
	payload := []byte("42.5")

	message := &mockMessage{
		topic:   topic,
		payload: payload,
	}

	messageHandler(nil, message)

	_, loaded := metrics.Load(topic)
	assert.False(t, loaded, "Метрика не должна быть создана при пустом топике")
}

func TestMessageHandler_InvalidPayload(t *testing.T) {
	topic := "test/topic"
	invalidPayload := []byte("not_a_number")

	message := &mockMessage{
		topic:   topic,
		payload: invalidPayload,
	}

	messageHandler(nil, message)

	val, loaded := metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть создана даже при невалидном payload")

	m := val.(*metricValue)
	assert.Equal(t, "text", m.valueType, "Тип метрики должен быть 'text' при невалидном payload")
	assert.Equal(t, "not_a_number", m.text, "Текст метрики должен соответствовать payload")
}

func TestCleanupTask(t *testing.T) {
	topic := "test/topic"
	payload := "42.5"

	processRegularMetric(topic, payload, 0)

	val, loaded := metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена")

	m := val.(*metricValue)
	m.updatedAt = time.Now().Add(-10 * time.Minute)

	httpRequestsOld = httpRequests.Load()
	cleanupTask(false, 5*time.Minute)

	_, loaded = metrics.Load(topic)
	assert.True(t, loaded, "Метрика опять должна быть загружена")

	httpRequestsOld++
	cleanupTask(false, 5*time.Minute)

	_, loaded = metrics.Load(topic)
	assert.False(t, loaded, "Метрика должна быть удалена после очистки")
}

func TestCleanupTaskNoCleanup(t *testing.T) {
	topic := "test/topic"
	payload := "42.5"

	processRegularMetric(topic, payload, 0)

	val, loaded := metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена")

	m := val.(*metricValue)
	m.updatedAt = time.Now().Add(-10 * time.Minute)

	cleanupTask(true, 5*time.Minute)

	_, loaded = metrics.Load(topic)
	assert.True(t, loaded, "Метрика не должна быть удалена после очистки")
}

func TestCleanupTask30m(t *testing.T) {
	topic := "test/topic"
	payload := "42.5"

	processRegularMetric(topic, payload, 0)

	val, loaded := metrics.Load(topic)
	assert.True(t, loaded, "Метрика должна быть загружена")

	m := val.(*metricValue)
	m.updatedAt = time.Now().Add(-10 * time.Minute)

	cleanupTask(false, 30*time.Minute)

	_, loaded = metrics.Load(topic)
	assert.True(t, loaded, "Метрика не должна быть удалена после очистки")

	cleanupTask(false, 5*time.Minute)

	_, loaded = metrics.Load(topic)
	assert.True(t, loaded, "Метрика не должна быть удалена после очистки")

	httpRequestsOld++
	cleanupTask(false, 5*time.Minute)

	_, loaded = metrics.Load(topic)
	assert.False(t, loaded, "Метрика должна быть удалена после очистки")
}

func TestCleanupTask_NoMetrics(t *testing.T) {
	metrics.Clear()

	cleanupTask(false, 5*time.Minute)

	assert.True(t, true, "Задача очистки должна завершиться без ошибок")
}

func TestCollectMemoryStats(t *testing.T) {
	collectMemoryStats()

	assert.NotZero(t, memStats.Alloc, "Статистика памяти должна быть собрана")
}

func TestSystemMetric(t *testing.T) {
	metric := systemMetric("gauge", 42.5)

	assert.Equal(t, "gauge", metric.Type, "Тип метрики должен быть 'gauge'")
	assert.Equal(t, 42.5, metric.Value, "Значение метрики должно быть 42.5")
}

func TestGetMetricValue(t *testing.T) {
	m := &metricValue{
		valueType: "number",
		number:    42.5,
	}

	value := getMetricValue(m)
	assert.Equal(t, 42.5, value, "Значение метрики должно быть 42.5")

	m = &metricValue{
		valueType: "text",
		text:      "testtext",
	}

	value = getMetricValue(m)
	assert.Equal(t, "testtext", value, "Значение метрики должно быть testtext")

	m = &metricValue{
		valueType: "counter",
		counter:   101,
	}

	value = getMetricValue(m)
	assert.Equal(t, uint64(101), value, "Значение метрики должно быть 101")
}

func TestGetMetricValue_InvalidType(t *testing.T) {
	m := &metricValue{
		valueType: "invalid_type",
	}

	value := getMetricValue(m)
	assert.Nil(t, value, "Значение должно быть nil при невалидном типе метрики")
}

func TestHTTPRequestsValue(t *testing.T) {
	assert.Equal(t, httpRequests.Load(), httpRequestsCounter.Load())
}

func TestJSONEncodingError(t *testing.T) {
	w := httptest.NewRecorder()
	r := &http.Request{}
	o := httpRequests.Load()
	topic := "/test/error/value"
	payload := math.NaN()
	metricVal := &metricValue{
		valueType: "number",
		updatedAt: time.Now(),
		number:    payload,
	}
	metrics.Store(topic, metricVal)
	metricsHandler(w, r)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, o, httpRequests.Load())
}

func TestParseFlags(t *testing.T) {
	parseFlags()
}

func TestMCStartErr(t *testing.T) {
	assert.Nil(t, mqttClient, "mqttClient должен быть nil!")
	broker := "tcp://localhost:1884"
	c, err := mcStart(broker, opts.topic)
	assert.Error(t, err, "Значение err не должно быть nil!")
	assert.NotNil(t, c, "c не должен быть nil!")
}

func TestMQTTConnectionsCounter(t *testing.T) {
	c := mqttConnections.Load()
	assert.Equal(t, uint64(0), c, "Значение mqttConnections должно быть 0")
}

func TestMQTTConnectedValue(t *testing.T) {
	c := mqttConnected.Load()
	assert.Equal(t, uint32(0), c, "Значение mqttConnected должно быть 0")
}

func TestMCStartOK(t *testing.T) {
	assert.Nil(t, mqttClient, "mqttClient должен быть nil!")
	broker := "tcp://test.mosquitto.org:1883"
	c, err := mcStart(broker, commit)
	assert.Nil(t, err, "Значение err должно быть nil!")
	assert.NotNil(t, c, "c не должен быть nil!")
	mqttClient = c
}

func TestMQTTConnectionsCounterOK(t *testing.T) {
	c := mqttConnections.Load()
	assert.Equal(t, uint64(1), c, "Значение mqttConnections должно быть 1")
}

func TestMQTTConnectedValueOK(t *testing.T) {
	c := mqttConnected.Load()
	assert.Equal(t, uint32(1), c, "Значение mqttConnected должно быть 1")
}

func TestMQTTDisconnect(t *testing.T) {
	broker := "wss://test.mosquitto.org:8081"
	//вызываем ошибку подключения из-за одинакового идентификатора
	_, _ = mcStart(broker, commit)
	assert.True(t, mqttClient.IsConnected())
	mqttClient.Disconnect(30000)
	assert.False(t, mqttClient.IsConnected())
}
