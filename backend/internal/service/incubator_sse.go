package service

import (
	"sync"

	"github.com/gin-gonic/gin"
)

// PipelineSSEClient represents a single SSE subscriber.
type PipelineSSEClient struct {
	Chan   chan string
	Writer gin.ResponseWriter
	Done   chan struct{}
}

// PipelineSSEHub manages SSE subscriptions for pipeline status updates.
// One job may have multiple subscribers (multiple browsers / users watching).
type PipelineSSEHub struct {
	mu      sync.RWMutex
	clients map[int64][]*PipelineSSEClient // key: pipeline job id
}

// NewPipelineSSEHub creates a new hub.
func NewPipelineSSEHub() *PipelineSSEHub {
	return &PipelineSSEHub{
		clients: make(map[int64][]*PipelineSSEClient),
	}
}

// Subscribe adds a new subscriber for a given pipeline job id.
func (h *PipelineSSEHub) Subscribe(jobID int64, w gin.ResponseWriter) *PipelineSSEClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	client := &PipelineSSEClient{
		Chan:   make(chan string, 10),
		Writer: w,
		Done:   make(chan struct{}),
	}
	h.clients[jobID] = append(h.clients[jobID], client)
	return client
}

// Unsubscribe removes a specific subscriber.
func (h *PipelineSSEHub) Unsubscribe(jobID int64, client *PipelineSSEClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.clients[jobID]
	for i, c := range list {
		if c == client {
			close(c.Done)
			// 不关闭 c.Chan：让 handler select 的 <-client.Done 分支自然退出，
			// 避免 Broadcast 向 closed channel 发送时 panic
			h.clients[jobID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(h.clients[jobID]) == 0 {
		delete(h.clients, jobID)
	}
}

// Broadcast sends an event to all subscribers of a given job.
// 使用 recover 兜底防止向已关闭的 channel 发送导致 panic。
func (h *PipelineSSEHub) Broadcast(jobID int64, event string) {
	h.mu.RLock()
	list := h.clients[jobID]
	h.mu.RUnlock()
	for _, client := range list {
		func() {
			defer func() {
				if recover() != nil {
					// channel closed, ignore silently
				}
			}()
			select {
			case client.Chan <- event:
			default: // drop if channel full (non-blocking)
			}
		}()
	}
}

// CloseAll closes all subscriptions.
func (h *PipelineSSEHub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, list := range h.clients {
		for _, client := range list {
			close(client.Done)
			close(client.Chan)
		}
	}
	h.clients = make(map[int64][]*PipelineSSEClient)
}
