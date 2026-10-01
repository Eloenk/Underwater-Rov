package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type LogLevel string

const (
	LevelInfo  LogLevel = "INFO"
	LevelWarn  LogLevel = "WARN"
	LevelError LogLevel = "ERROR"
	LevelDebug LogLevel = "DEBUG"
)

type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Module    string    `json:"module"`
	Level     LogLevel  `json:"level"`
	Message   string    `json:"message"`
}

type EventEntry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Module    string    `json:"module"`
	EventType string    `json:"event_type"`
	Details   string    `json:"details"`
}

type RingBuffer struct {
	mu       sync.RWMutex
	entries  []LogEntry
	capacity int
	head     int
	count    int
}

func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1000
	}
	return &RingBuffer{
		entries:  make([]LogEntry, capacity),
		capacity: capacity,
	}
}

func (rb *RingBuffer) Add(entry LogEntry) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.entries[rb.head] = entry
	rb.head = (rb.head + 1) % rb.capacity
	if rb.count < rb.capacity {
		rb.count++
	}
}

func (rb *RingBuffer) Tail(n int) []LogEntry {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if n <= 0 || n > rb.count {
		n = rb.count
	}

	result := make([]LogEntry, n)
	start := (rb.head - rb.count + rb.capacity) % rb.capacity
	offset := rb.count - n
	actualStart := (start + offset) % rb.capacity

	for i := 0; i < n; i++ {
		result[i] = rb.entries[(actualStart+i)%rb.capacity]
	}
	return result
}

type LogManager struct {
	mu           sync.RWMutex
	buffers      map[string]*RingBuffer
	globalBuffer *RingBuffer
	subscribers  map[chan LogEntry]struct{}
	events       []EventEntry
	capacity     int
	eventDBPath  string
	nextEventID  int64
	stdoutOutput bool
}

func NewLogManager(capacity int) *LogManager {
	if capacity <= 0 {
		capacity = 1000
	}
	return &LogManager{
		buffers:      make(map[string]*RingBuffer),
		globalBuffer: NewRingBuffer(capacity),
		subscribers:  make(map[chan LogEntry]struct{}),
		events:       make([]EventEntry, 0, capacity),
		capacity:     capacity,
		stdoutOutput: true,
	}
}

func (lm *LogManager) SetEventDBPath(path string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.eventDBPath = path

	if data, err := os.ReadFile(path); err == nil {
		var savedEvents []EventEntry
		if err := json.Unmarshal(data, &savedEvents); err == nil {
			if len(savedEvents) > lm.capacity {
				savedEvents = savedEvents[len(savedEvents)-lm.capacity:]
			}
			lm.events = savedEvents
			if len(savedEvents) > 0 {
				lm.nextEventID = savedEvents[len(savedEvents)-1].ID
			}
		}
	}
}

func (lm *LogManager) Log(module string, level LogLevel, message string) {
	entry := LogEntry{
		Timestamp: time.Now().UTC(),
		Module:    module,
		Level:     level,
		Message:   message,
	}

	lm.mu.Lock()
	buf, exists := lm.buffers[module]
	if !exists {
		buf = NewRingBuffer(lm.capacity)
		lm.buffers[module] = buf
	}
	lm.mu.Unlock()

	buf.Add(entry)
	lm.globalBuffer.Add(entry)

	lm.mu.RLock()
	for ch := range lm.subscribers {
		select {
		case ch <- entry:
		default:
		}
	}
	lm.mu.RUnlock()

	if lm.stdoutOutput {
		out, _ := json.Marshal(entry)
		fmt.Println(string(out))
	}
}

func (lm *LogManager) RecordEvent(module, eventType, details string) {
	lm.mu.Lock()
	lm.nextEventID++
	event := EventEntry{
		ID:        lm.nextEventID,
		Timestamp: time.Now().UTC(),
		Module:    module,
		EventType: eventType,
		Details:   details,
	}

	if len(lm.events) >= lm.capacity {
		lm.events = lm.events[1:]
	}
	lm.events = append(lm.events, event)
	eventDBPath := lm.eventDBPath
	eventsSnapshot := make([]EventEntry, len(lm.events))
	copy(eventsSnapshot, lm.events)
	lm.mu.Unlock()

	if eventDBPath != "" {
		go func(path string, evs []EventEntry) {
			if data, err := json.MarshalIndent(evs, "", "  "); err == nil {
				_ = os.WriteFile(path, data, 0644)
			}
		}(eventDBPath, eventsSnapshot)
	}

	lm.Log(module, LevelInfo, fmt.Sprintf("[EVENT: %s] %s", eventType, details))
}

func (lm *LogManager) Subscribe() chan LogEntry {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	ch := make(chan LogEntry, 100)
	lm.subscribers[ch] = struct{}{}
	return ch
}

func (lm *LogManager) Unsubscribe(ch chan LogEntry) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	delete(lm.subscribers, ch)
	close(ch)
}

func (lm *LogManager) TailModule(module string, n int) []LogEntry {
	lm.mu.RLock()
	buf, exists := lm.buffers[module]
	lm.mu.RUnlock()

	if !exists {
		return []LogEntry{}
	}
	return buf.Tail(n)
}

func (lm *LogManager) TailGlobal(n int) []LogEntry {
	return lm.globalBuffer.Tail(n)
}

func (lm *LogManager) GetEvents() []EventEntry {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	result := make([]EventEntry, len(lm.events))
	copy(result, lm.events)
	return result
}


