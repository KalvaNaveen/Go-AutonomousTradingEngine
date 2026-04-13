package storage

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"net/url"
	"sync"
	"time"

	"bnf_go_engine/config"

	"github.com/gorilla/websocket"
)

// KiteWebSocket connects to Zerodha Kite Streaming API
// and feeds ticks into the TickStore at wire speed
type KiteWebSocket struct {
	apiKey      string
	accessToken string
	store       *TickStore
	tokens      []uint32
	conn        *websocket.Conn
	mu          sync.Mutex
	connected   bool
	reconnects  int
}

func NewKiteWebSocket(store *TickStore, tokens []uint32) *KiteWebSocket {
	return &KiteWebSocket{
		apiKey:      config.KiteAPIKey,
		accessToken: config.KiteAccessToken,
		store:       store,
		tokens:      tokens,
	}
}

// Connect establishes WebSocket connection and starts reading
func (kws *KiteWebSocket) Connect() error {
	u := url.URL{
		Scheme: "wss",
		Host:   "ws.kite.trade",
		Path:   "/",
		RawQuery: fmt.Sprintf("api_key=%s&access_token=%s",
			kws.apiKey, kws.accessToken),
	}

	log.Printf("[WS] Connecting to %s", u.Host)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("ws connect failed: %v", err)
	}

	kws.mu.Lock()
	kws.conn = conn
	kws.connected = true
	kws.mu.Unlock()

	log.Printf("[WS] Connected. Subscribing %d tokens in FULL mode", len(kws.tokens))

	// Subscribe all tokens in FULL mode
	kws.subscribe(kws.tokens)
	kws.setMode("full", kws.tokens)

	// Start reading in goroutine IF this is the first connection
	// We'll manage goroutines carefully so we don't leak them
	if !kws.connected { // Safety check to prevent double-spawning if Connect is called externally
		kws.connected = true
		go kws.readLoop()
	}

	return nil
}

func (kws *KiteWebSocket) subscribe(tokens []uint32) {
	// Kite subscribe message: {"a": "subscribe", "v": [token1, token2, ...]}
	msg := fmt.Sprintf(`{"a":"subscribe","v":[`)
	for i, t := range tokens {
		if i > 0 {
			msg += ","
		}
		msg += fmt.Sprintf("%d", t)
	}
	msg += "]}"

	kws.mu.Lock()
	defer kws.mu.Unlock()
	if kws.conn != nil {
		kws.conn.WriteMessage(websocket.TextMessage, []byte(msg))
	}
}

func (kws *KiteWebSocket) setMode(mode string, tokens []uint32) {
	msg := fmt.Sprintf(`{"a":"mode","v":["%s",[`, mode)
	for i, t := range tokens {
		if i > 0 {
			msg += ","
		}
		msg += fmt.Sprintf("%d", t)
	}
	msg += "]]}"

	kws.mu.Lock()
	defer kws.mu.Unlock()
	if kws.conn != nil {
		kws.conn.WriteMessage(websocket.TextMessage, []byte(msg))
	}
}

func (kws *KiteWebSocket) readLoop() {
	defer func() {
		kws.mu.Lock()
		kws.connected = false
		kws.mu.Unlock()
	}()

	for {
		// Set a read deadline. Kite sends ticks/heartbeats every second during market hours.
		// If 15 seconds pass without ANY message, the network TCP connection is silently hung.
		kws.mu.Lock()
		conn := kws.conn
		kws.mu.Unlock()
		
		if conn == nil {
			return
		}
		
		conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[WS] Read error/timeout: %v. Reconnecting...", err)
			
			// Try to reconnect blocking
			if kws.reconnect() {
				// Reconnect succeeded, continue the SAME readLoop goroutine 
				continue
			} else {
				// 300 attempts failed (e.g. overnight). Exit goroutine.
				return
			}
		}

		// Binary message = tick data
		if len(message) < 2 {
			continue
		}

		// Parse binary tick packets
		if len(message) >= 2 {
			numPackets := int(binary.BigEndian.Uint16(message[:2]))
			offset := 2
			for i := 0; i < numPackets && offset < len(message); i++ {
				if offset+2 > len(message) {
					break
				}
				pktLen := int(binary.BigEndian.Uint16(message[offset : offset+2]))
				offset += 2
				if offset+pktLen > len(message) {
					break
				}
				pkt := message[offset : offset+pktLen]
				offset += pktLen

				kws.parseTick(pkt)
			}
		}
	}
}

// parseTick decodes a single Kite tick binary packet
func (kws *KiteWebSocket) parseTick(pkt []byte) {
	if len(pkt) < 8 {
		return
	}

	token := uint32(binary.BigEndian.Uint32(pkt[0:4]))
	ltp := float64(int32(binary.BigEndian.Uint32(pkt[4:8]))) / 100.0

	var vol int64
	var dayOpen, dayHigh, dayLow, changePct float64
	var bidQty, askQty int64
	var exchangeTS time.Time

	// FULL mode packet (184 bytes for equity)
	if len(pkt) >= 44 {
		vol = int64(binary.BigEndian.Uint32(pkt[16:20]))
		dayOpen = float64(int32(binary.BigEndian.Uint32(pkt[20:24]))) / 100.0
		dayHigh = float64(int32(binary.BigEndian.Uint32(pkt[24:28]))) / 100.0
		dayLow = float64(int32(binary.BigEndian.Uint32(pkt[28:32]))) / 100.0
	}

	// Close price for change calculation
	if len(pkt) >= 36 {
		closePrice := float64(int32(binary.BigEndian.Uint32(pkt[32:36]))) / 100.0
		if closePrice > 0 {
			changePct = ((ltp - closePrice) / closePrice) * 100.0
		}
	}

	// Exchange timestamp
	if len(pkt) >= 44 {
		epochTS := int64(binary.BigEndian.Uint32(pkt[40:44]))
		if epochTS > 0 {
			exchangeTS = time.Unix(epochTS, 0).In(config.IST)
		}
	}

	// Depth data (FULL mode: 20 depth entries starting at offset 44)
	if len(pkt) >= 184 {
		// 5 buy levels + 5 sell levels, each 12 bytes
		for i := 0; i < 5; i++ {
			off := 44 + i*12
			qty := int64(binary.BigEndian.Uint32(pkt[off : off+4]))
			bidQty += qty
		}
		for i := 0; i < 5; i++ {
			off := 44 + 60 + i*12 // sell starts after 5 buy entries
			qty := int64(binary.BigEndian.Uint32(pkt[off : off+4]))
			askQty += qty
		}
	}

	// Feed into TickStore at wire speed (nanosecond path)
	kws.store.OnTick(token, ltp, vol, dayOpen, dayHigh, dayLow, changePct, bidQty, askQty, exchangeTS)
}

func (kws *KiteWebSocket) reconnect() bool {
	for attempt := 1; attempt <= 300; attempt++ {
		backoff := time.Duration(min(attempt*2, 30)) * time.Second
		log.Printf("[WS] Reconnect attempt %d/300 in %v", attempt, backoff)
		time.Sleep(backoff)

		if err := kws.Connect(); err != nil {
			log.Printf("[WS] Reconnect failed: %v", err)
			continue
		}

		kws.mu.Lock()
		kws.reconnects = 0
		kws.mu.Unlock()
		log.Printf("[WS] Reconnected successfully after %d attempts", attempt)
		return true
	}
	log.Println("[WS] CRITICAL: Failed to reconnect after 300 attempts")
	return false
}

func (kws *KiteWebSocket) IsConnected() bool {
	kws.mu.Lock()
	defer kws.mu.Unlock()
	return kws.connected
}

func (kws *KiteWebSocket) Close() {
	kws.mu.Lock()
	defer kws.mu.Unlock()
	if kws.conn != nil {
		kws.conn.Close()
		kws.connected = false
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Suppress unused import
var _ = math.Abs
