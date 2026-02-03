package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-meshcore-webgui/app/models"
	"go-meshcore-webgui/app/storage"
)

// MeshcoreClient is the interface for interacting with a meshcore node.
type MeshcoreClient interface {
	Connect(host string, port int) error
	Disconnect() error
	IsConnected() bool
	Address() string
	FirmwareVersion() string
	GetDeviceInfo() models.DeviceInfo
	GetContacts() ([]models.Contact, error)
	GetChannels() ([]models.Channel, error)
	GetRecentMessages() ([]models.Message, error)
	GetFavorites() ([]string, error)
	ToggleFavorite(contactID string) error
	SendMessage(toID string, isChannel bool, content string) (models.Message, error)
	SetChannel(channelIdx int, name string, secret []byte) error
	ClearChannelMessages(channelID string) error
	EnablePushMode()
	FetchWaitingMessages() // Proactively fetch any waiting messages from device
	SendZeroHopAdvert() error
	SendFloodAdvert() error

	// Register a callback invoked for each newly received incoming message
	// from the node (contact or channel message).
	SetIncomingMessageHandler(func(models.Message))
}

type waiter struct {
	code byte
	ch   chan []byte
}

// InMemoryMeshcoreClient is a placeholder for the real meshcore integration.
// It now handles the low-level protocol communication.
type InMemoryMeshcoreClient struct {
	mu       sync.Mutex
	ioMu     sync.Mutex // Separate mutex for device I/O operations
	pushMode bool       // Track if push mode (readLoop) is active
	contacts []models.Contact
	channels []models.Channel

	addr          string
	conn          net.Conn
	connected     bool
	deviceInfo    models.DeviceInfo
	messageStore  *storage.MessageStore
	publicKeyHex  string // used to identify which node we're connected to

	// Optional callback for newly received incoming messages.
	onIncomingMessage func(models.Message)

	// Optional callback for message status updates (sent/delivered)
	onMessageStatusUpdate func(messageID string, status string, roundTripMs uint32)

	frameCh       chan []byte   // central stream of incoming frames
	stopReader    chan struct{} // to stop read loop
	readerStopped bool          // track if stopReader is already closed
	waitersMu     sync.Mutex
	waiters       map[byte][]chan []byte

	// Track pending messages by ackCode
	ackCodeMu        sync.Mutex
	ackCodeCounter   uint32
	ackCodeToMsgID   map[uint32]string // Map ackCode to message ID
	recentlySentMsgs []string          // Queue of recently sent message IDs (FIFO)
}

func NewInMemoryMeshcoreClient() *InMemoryMeshcoreClient {
	return &InMemoryMeshcoreClient{
		contacts: []models.Contact{
			{ID: "c1", Name: "Alice"},
			{ID: "c2", Name: "Bob"},
		},
		channels: []models.Channel{
			{ID: "ch1", Name: "General"},
			{ID: "ch2", Name: "Ops"},
			{ID: "debug", Name: "Debug"}, // Virtual channel for debugging
		},
		frameCh:         make(chan []byte, 128),
		stopReader:      make(chan struct{}),
		waiters:         make(map[byte][]chan []byte),
		ackCodeToMsgID:  make(map[uint32]string),
		ackCodeCounter:  1, // Start from 1
	}
}

// EnablePushMode starts the background read loop used to receive pushes.
// Call this after you've finished any synchronous readFrame-based setup
// (e.g. initial contacts/channels sync).
func (m *InMemoryMeshcoreClient) EnablePushMode() {
	m.mu.Lock()
	if m.conn == nil {
		m.mu.Unlock()
		return
	}
	if m.pushMode {
		m.mu.Unlock()
		return // Already in push mode
	}
	m.pushMode = true
	m.mu.Unlock()
	
	// Start the readLoop
	go m.readLoop()
}

// FetchWaitingMessages proactively fetches any messages waiting on the device.
// This should be called after EnablePushMode to retrieve historical messages
// that may not have triggered a PushMsgWaiting notification.
func (m *InMemoryMeshcoreClient) FetchWaitingMessages() {
	log.Println("FetchWaitingMessages: proactively checking for waiting messages")
	go m.drainWaitingMessages()
}

// SetIncomingMessageHandler registers a callback for newly received messages.
func (m *InMemoryMeshcoreClient) SetIncomingMessageHandler(fn func(models.Message)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onIncomingMessage = fn
}

// SetMessageStatusUpdateHandler registers a callback for message status updates.
func (m *InMemoryMeshcoreClient) SetMessageStatusUpdateHandler(fn func(messageID string, status string, roundTripMs uint32)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onMessageStatusUpdate = fn
}

// Connect opens a TCP connection to the meshcore node and performs a handshake.
func (m *InMemoryMeshcoreClient) Connect(host string, port int) error {
	m.mu.Lock()
	if m.conn != nil {
		_ = m.conn.Close()
		m.conn = nil
		m.connected = false
		m.addr = ""
		m.deviceInfo = models.DeviceInfo{}
	}
	// Reset stopReader channel for new connection
	m.stopReader = make(chan struct{})
	m.readerStopped = false
	m.mu.Unlock()

	if host == "" {
		host = "127.0.0.1"
	}
	if port == 0 {
		port = 5000
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("tcp connect failed: %w", err)
	}

	info, err := performMeshcoreHandshake(conn)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("meshcore handshake failed: %w", err)
	}

	// Get the node's public key to use as identifier for storage
	publicKeyHex := getNodePublicKeyHex(info)

	// Initialize message store for this node
	msgStore, err := storage.NewMessageStore(publicKeyHex)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to initialize message store: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.conn = conn
	m.connected = true
	m.addr = addr
	m.deviceInfo = info
	m.messageStore = msgStore
	m.publicKeyHex = publicKeyHex
	log.Printf("connected to meshcore node at %s (fw: %s)\\n", addr, info.FirmwareVersion)
	// TEMP: disable background reader so synchronous readFrame-based methods keep working.
	// go m.readLoop()
	return nil
}

func (m *InMemoryMeshcoreClient) readLoop() {
	for {
		frame, err := m.readFrame()
		if err != nil {
			// Treat read timeouts as idle periods, not fatal errors.
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				log.Printf("readLoop: idle timeout waiting for frames; continuing to listen")
				continue
			}
			log.Printf("readLoop error: %v; stopping reader", err)
			return
		}
		m.deliverFrame(frame)
	}
}

func (m *InMemoryMeshcoreClient) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.conn != nil {
		// Close stopReader channel to signal readLoop to stop (if it's running)
		if !m.readerStopped && m.stopReader != nil {
			close(m.stopReader)
			m.readerStopped = true
		}
		err := m.conn.Close()
		m.conn = nil
		m.connected = false
		m.pushMode = false
		m.addr = ""
		log.Printf("disconnected from meshcore node")
		return err
	}

	m.connected = false
	m.pushMode = false
	m.addr = ""
	return nil
}

func (m *InMemoryMeshcoreClient) IsConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

func (m *InMemoryMeshcoreClient) Address() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addr
}

func (m *InMemoryMeshcoreClient) FirmwareVersion() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deviceInfo.FirmwareVersion
}

func (m *InMemoryMeshcoreClient) GetDeviceInfo() models.DeviceInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deviceInfo
}

func (m *InMemoryMeshcoreClient) sendFrame(payload []byte) error {
	// Get connection with mutex protection
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	
	if conn == nil {
		return errors.New("not connected")
	}
	
	frame := make([]byte, 3+len(payload))
	frame[0] = 0x3c
	binary.LittleEndian.PutUint16(frame[1:3], uint16(len(payload)))
	copy(frame[3:], payload)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := conn.Write(frame)
	log.Printf("--> SENT FRAME: len=%d, data=%x", len(payload), frame)
	if err != nil {
		return fmt.Errorf("send failed: %w", err)
	}
	return err
}

func (m *InMemoryMeshcoreClient) readFrame() ([]byte, error) {
	// Get connection with mutex protection
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	
	if conn == nil {
		return nil, errors.New("not connected")
	}
	
	head := make([]byte, 3)
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.ReadFull(conn, head); err != nil {
		return nil, err
	}
	if head[0] != 0x3e {
		return nil, fmt.Errorf("unexpected frame prefix: 0x%02x", head[0])
	}
	sz := binary.LittleEndian.Uint16(head[1:3])
	if sz > 4096 {
		return nil, fmt.Errorf("frame too large: %d", sz)
	}
	buf := make([]byte, sz)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	log.Printf("<-- RECV FRAME: len=%d, data=%x", sz, buf)
	return buf, nil
}

func (m *InMemoryMeshcoreClient) getContactsFromDevice() ([]models.Contact, error) {
	if m.conn == nil {
		return nil, errors.New("not connected")
	}

	// Match meshcore.js: send a single GetContacts command (0x04) with no "since" field,
	// then passively consume ContactsStart/Contact/EndOfContacts responses.
	getContactsCmd := []byte{models.CommandCodes["GetContacts"]}
	log.Printf("--> Sending GET_CONTACTS command: %x", getContactsCmd)
	if err := m.sendFrame(getContactsCmd); err != nil {
		return nil, fmt.Errorf("send GET_CONTACTS failed: %w", err)
	}
	log.Printf("-- sent GET_CONTACTS command")

	var contacts []models.Contact
	var totalCount uint32
	log.Println("... Reading contact list stream (ContactsStart -> Contact* -> EndOfContacts)")

	for {
		resp, err := m.readFrame()

		if err != nil {
			return nil, fmt.Errorf("read contact frame failed: %w", err)
		}
		if len(resp) == 0 {
			log.Println("received empty contact frame, ignoring")
			continue
		}

		packetType := resp[0]

		switch packetType {
		case byte(models.ResponseCodes.ContactsStart):
			// First response: ContactsStart (0x02) + uint32 count (LE)
			if len(resp) < 5 {
				log.Printf("ContactsStart packet too short: %d bytes", len(resp))
				continue
			}
			count := binary.LittleEndian.Uint32(resp[1:5])
			totalCount = count
			log.Printf("ContactsStart: device reports %d contacts", totalCount)

		case byte(models.ResponseCodes.Contact):
			// Contact entry: parse MeshContact from resp[1:]
			if len(resp) <= 1 {
				log.Println("Contact packet too short, ignoring")
				continue
			}

			var meshContact models.MeshContact
			reader := bytes.NewReader(resp[1:])
			if err := binary.Read(reader, binary.LittleEndian, &meshContact); err != nil {
				log.Printf("failed to parse MeshContact from binary data: %v", err)
				continue
			}

			contactID := fmt.Sprintf("!%x", meshContact.PublicKey)
			name := string(bytes.Trim(meshContact.AdvName[:], "\\x00"))
			if name == "" {
				name = contactID
			}

			// Convert LastMod (Unix timestamp) to time.Time
			lastSeen := time.Unix(int64(meshContact.LastMod), 0)

			contact := models.Contact{
				ID:       contactID,
				Name:     name,
				LastSeen: lastSeen,
			}
			contacts = append(contacts, contact)

		case byte(models.ResponseCodes.EndOfContacts):
			log.Printf("... Received EndOfContacts, found %d contacts (device reported %d)", len(contacts), totalCount)
			m.contacts = contacts
			log.Printf("getContactsFromDevice() parsed %d contacts\\n", len(contacts))
			return contacts, nil

		case byte(models.ResponseCodes.SelfInfo):
			// Some firmware may send SELF_INFO around contacts; ignore for listing.
			log.Println("... Received SELF_INFO while fetching contacts, ignoring.")
			continue

		default:
			// Check if this is a push frame (0x80+) or other unsolicited response
			if packetType >= 0x80 || packetType == byte(models.ResponseCodes.ChannelInfo) {
				log.Printf("... Received unsolicited frame 0x%02x while fetching contacts, skipping", packetType)
				continue
			}
			log.Printf("ignoring unexpected packet type 0x%02x while fetching contacts", packetType)
		}
	}
}

func (m *InMemoryMeshcoreClient) GetContacts() ([]models.Contact, error) {
	log.Println("GetContacts() called")
	// Check connection status with mutex, then release it for I/O
	m.mu.Lock()
	if m.conn == nil {
		m.mu.Unlock()
		return nil, errors.New("not connected")
	}
	m.mu.Unlock()
	
	// Serialize device I/O operations
	m.ioMu.Lock()
	defer m.ioMu.Unlock()
	
	return m.getContactsFromDevice()
}

func (m *InMemoryMeshcoreClient) getContactByID(contactID string) (*models.Contact, error) {
	for _, c := range m.contacts {
		if c.ID == contactID {
			return &c, nil
		}
	}
	// If not in cache, try fetching from the device
	_, err := m.getContactsFromDevice()
	if err != nil {
		return nil, err
	}
	for _, c := range m.contacts {
		if c.ID == contactID {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("contact with ID %s not found", contactID)
}

func (m *InMemoryMeshcoreClient) getChannel(channelIndex int) (*models.Channel, error) {
	getChannelCmd := []byte{models.CommandCodes["GetChannel"], byte(channelIndex)}
	log.Printf("--> Sending GET_CHANNEL command for index %d: %x", channelIndex, getChannelCmd)
	
	// Check if we're in push mode
	m.mu.Lock()
	inPushMode := m.pushMode
	m.mu.Unlock()
	
	var resp []byte
	var err error
	
	if inPushMode {
		// Use waiter mechanism if readLoop is active
		channelInfoCode := byte(models.ResponseCodes.ChannelInfo)
		waiterCh := m.addWaiter(channelInfoCode)
		defer m.removeWaiter(channelInfoCode, waiterCh)
		
		if err := m.sendFrame(getChannelCmd); err != nil {
			return nil, fmt.Errorf("send GET_CHANNEL for index %d failed: %w", channelIndex, err)
		}
		
		// Wait for response via waiter channel
		select {
		case resp = <-waiterCh:
			if len(resp) == 0 || resp[0] != byte(models.ResponseCodes.ChannelInfo) {
				return nil, fmt.Errorf("expected CHANNEL_INFO but got type 0x%02x", resp[0])
			}
		case <-time.After(30 * time.Second):
			return nil, fmt.Errorf("timeout waiting for GET_CHANNEL response for index %d", channelIndex)
		}
	} else {
		// Use direct readFrame if not in push mode yet
		if err := m.sendFrame(getChannelCmd); err != nil {
			return nil, fmt.Errorf("send GET_CHANNEL for index %d failed: %w", channelIndex, err)
		}
		
		resp, err = m.readFrame()
		if err != nil {
			return nil, fmt.Errorf("read GET_CHANNEL response for index %d failed: %w", channelIndex, err)
		}
		
		if len(resp) == 0 || resp[0] != byte(models.ResponseCodes.ChannelInfo) {
			return nil, fmt.Errorf("expected CHANNEL_INFO but got type 0x%02x", resp[0])
		}
	}

	if len(resp) < 34 { // 1 byte type + 1 byte index + 32 byte name
		return nil, fmt.Errorf("CHANNEL_INFO packet too short: %d", len(resp))
	}

	if len(resp) < 34 { // 1 byte type + 1 byte index + 32 byte name
		return nil, fmt.Errorf("CHANNEL_INFO packet too short: %d", len(resp))
	}

	index := int(resp[1])
	// Name is a C string (null-terminated) in a 32-byte buffer
	nameBytes := resp[2:34]
	// Find null terminator
	nullIdx := bytes.IndexByte(nameBytes, 0)
	if nullIdx >= 0 {
		nameBytes = nameBytes[:nullIdx]
	}
	name := string(nameBytes)
	if name == "" {
		name = fmt.Sprintf("Channel %d", index)
	}

	channel := models.Channel{
		ID:   fmt.Sprintf("ch%d", index),
		Name: name,
	}
	log.Printf("... Parsed channel: ID=%s, Name=%s", channel.ID, channel.Name)

	return &channel, nil
}

func (m *InMemoryMeshcoreClient) GetChannels() ([]models.Channel, error) {
	log.Println("GetChannels() called")
	
	// Check connection status and get maxChannels with mutex, then release for I/O
	m.mu.Lock()
	if m.conn == nil {
		m.mu.Unlock()
		return nil, errors.New("not connected")
	}
	maxChannels := m.deviceInfo.MaxChannels
	if maxChannels == 0 {
		// Fallback to 4 if device info not available
		maxChannels = 4
	}
	m.mu.Unlock()
	
	// Serialize device I/O operations
	m.ioMu.Lock()
	defer m.ioMu.Unlock()

	var channels []models.Channel
	log.Printf("Querying %d channel slots from device", maxChannels)
	
	for i := 0; i < maxChannels; i++ {
		channel, err := m.getChannel(i)
		if err != nil {
			// Channel slot not configured or error
			log.Printf("Channel index %d not available: %v", i, err)
			continue
		}
		channels = append(channels, *channel)
	}

	// Update cache with mutex
	m.mu.Lock()
	m.channels = channels
	m.mu.Unlock()
	
	// Always append the virtual Debug channel
	channels = append(channels, models.Channel{
		ID:   "debug",
		Name: "Debug",
	})
	
	log.Printf("GetChannels() found %d channels (+ Debug)\\n", len(channels)-1)
	return channels, nil
}

func (m *InMemoryMeshcoreClient) GetRecentMessages() ([]models.Message, error) {
	m.mu.Lock()
	store := m.messageStore
	m.mu.Unlock()

	if store == nil {
		return []models.Message{}, nil
	}

	return store.GetMessages(), nil
}

func (m *InMemoryMeshcoreClient) GetFavorites() ([]string, error) {
	m.mu.Lock()
	store := m.messageStore
	m.mu.Unlock()

	if store == nil {
		return []string{}, nil
	}

	return store.GetFavorites(), nil
}

func (m *InMemoryMeshcoreClient) ToggleFavorite(contactID string) error {
	m.mu.Lock()
	store := m.messageStore
	m.mu.Unlock()

	if store == nil {
		return errors.New("message store not initialized")
	}

	// Check if already a favorite
	favorites := store.GetFavorites()
	isFavorite := false
	for _, id := range favorites {
		if id == contactID {
			isFavorite = true
			break
		}
	}

	if isFavorite {
		return store.RemoveFavorite(contactID)
	}
	return store.AddFavorite(contactID)
}

func (m *InMemoryMeshcoreClient) SendMessage(toID string, isChannel bool, content string) (models.Message, error) {
	// Check connection without holding mutex during I/O
	m.mu.Lock()
	if m.conn == nil {
		m.mu.Unlock()
		return models.Message{}, errors.New("not connected")
	}
	m.mu.Unlock()

	var framePayload []byte

	if isChannel {
		// Check if this is the debug channel (virtual channel)
		if toID == "debug" {
			return models.Message{}, errors.New("cannot send messages to debug channel")
		}

		cmd := models.CMD_SendChannelTxtMsg

		channelID, err := strconv.Atoi(strings.TrimPrefix(toID, "ch"))
		if err != nil {
			return models.Message{}, fmt.Errorf("invalid channel ID: %s", toID)
		}

		var buf bytes.Buffer
		buf.WriteByte(byte(cmd))       // SendChannelTxtMsg (0x03)
		buf.WriteByte(0)               // txtType = Plain
		buf.WriteByte(byte(channelID)) // channelIdx
		if err := binary.Write(&buf, binary.LittleEndian, uint32(time.Now().Unix())); err != nil {
			return models.Message{}, fmt.Errorf("failed to write timestamp: %w", err)
		}
		buf.WriteString(content) // text

		framePayload = buf.Bytes()
		log.Printf("--> Sending channel message: payload=%x", framePayload)

	} else {
		// This block is structured to match the provided JavaScript snippet for sending a node-to-node message.
		contact, err := m.getContactByID(toID)
		if err != nil {
			return models.Message{}, err
		}

		pubKeyBytes, err := hex.DecodeString(strings.TrimPrefix(contact.ID, "!"))
		if err != nil {
			return models.Message{}, fmt.Errorf("invalid contact ID format: %s", contact.ID)
		}

		var buf bytes.Buffer
		buf.WriteByte(models.CMD_SendTxtMsg)
		buf.WriteByte(0) // txtType
		buf.WriteByte(0) // attempt
		if err := binary.Write(&buf, binary.LittleEndian, uint32(time.Now().Unix())); err != nil {
			return models.Message{}, fmt.Errorf("failed to write timestamp: %w", err)
		}
		buf.Write(pubKeyBytes[:6]) // pubKeyPrefix
		buf.WriteString(content)   // text

		framePayload = buf.Bytes()
		log.Printf("--> Sending message: payload=%x", framePayload)
	}

	if err := m.sendFrame(framePayload); err != nil {
		return models.Message{}, fmt.Errorf("send message failed: %w", err)
	}

	// Create the message with pending status
	msg := models.Message{
		ID:        time.Now().Format("20060102150405.000000"),
		From:      "self",
		To:        toID,
		IsChannel: isChannel,
		Content:   content,
		Timestamp: time.Now().UTC(),
		Status:    "pending",
	}

	// For contact messages, add to recently sent queue so we can match it with the Sent response
	if !isChannel {
		m.ackCodeMu.Lock()
		m.recentlySentMsgs = append(m.recentlySentMsgs, msg.ID)
		// Keep queue size reasonable (last 100 messages)
		if len(m.recentlySentMsgs) > 100 {
			m.recentlySentMsgs = m.recentlySentMsgs[1:]
		}
		m.ackCodeMu.Unlock()
		log.Printf("Added message %s to recently sent queue", msg.ID)
	}

	m.mu.Lock()
	store := m.messageStore
	m.mu.Unlock()
	
	if store != nil {
		if err := store.AddMessage(msg); err != nil {
			log.Printf("warning: failed to persist sent message: %v", err)
		}
	}

	return msg, nil
}

func (m *InMemoryMeshcoreClient) ClearChannelMessages(channelID string) error {
	m.mu.Lock()
	store := m.messageStore
	m.mu.Unlock()

	if store == nil {
		return errors.New("message store not initialized")
	}

	return store.RemoveChannelMessages(channelID)
}

func (m *InMemoryMeshcoreClient) SetChannel(channelIdx int, name string, secret []byte) error {
	if m.conn == nil {
		return errors.New("not connected")
	}

	if len(secret) != 16 {
		return fmt.Errorf("secret must be exactly 16 bytes, got %d", len(secret))
	}

	// Register waiters for OK and ERR responses
	chOk := m.addWaiter(byte(models.ResponseCodes.Ok))
	chErr := m.addWaiter(byte(models.ResponseCodes.Err))
	defer func() {
		m.removeWaiter(byte(models.ResponseCodes.Ok), chOk)
		m.removeWaiter(byte(models.ResponseCodes.Err), chErr)
		// Drain channels
		select {
		case <-chOk:
		default:
		}
		select {
		case <-chErr:
		default:
		}
	}()

	// Prepare the SetChannel command payload
	var buf bytes.Buffer
	buf.WriteByte(models.CMD_SetChannel) // 0x20
	buf.WriteByte(byte(channelIdx))

	// Write name as C string (null-terminated, padded to 32 bytes)
	nameBytes := make([]byte, 32)
	copy(nameBytes, []byte(name))
	buf.Write(nameBytes)

	// Write 16-byte secret
	buf.Write(secret)

	m.mu.Lock()
	if err := m.sendFrame(buf.Bytes()); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("failed to send SetChannel command: %w", err)
	}
	m.mu.Unlock()

	// Wait for OK or ERR response with timeout
	timeout := time.After(5 * time.Second)
	select {
	case resp := <-chOk:
		if len(resp) > 0 && resp[0] == byte(models.ResponseCodes.Ok) {
			log.Printf("Channel %d (%s) set successfully", channelIdx, name)
			return nil
		}
		return errors.New("invalid OK response")
	case resp := <-chErr:
		if len(resp) >= 2 {
			return fmt.Errorf("node returned error for SetChannel: code 0x%02x", resp[1])
		}
		return errors.New("node returned error for SetChannel")
	case <-timeout:
		return errors.New("timeout waiting for SetChannel response")
	}
}

func (m *InMemoryMeshcoreClient) addWaiter(code byte) <-chan []byte {
	ch := make(chan []byte, 1)
	m.waitersMu.Lock()
	defer m.waitersMu.Unlock()
	m.waiters[code] = append(m.waiters[code], ch)
	return ch
}

func (m *InMemoryMeshcoreClient) removeWaiter(code byte, ch <-chan []byte) {
	m.waitersMu.Lock()
	defer m.waitersMu.Unlock()
	
	chans := m.waiters[code]
	for i, c := range chans {
		if c == ch {
			// Remove this waiter from the slice
			m.waiters[code] = append(chans[:i], chans[i+1:]...)
			log.Printf("removeWaiter: removed waiter for code=0x%02x", code)
			break
		}
	}
}

func (m *InMemoryMeshcoreClient) deliverFrame(frame []byte) {
	if len(frame) == 0 {
		return
	}
	code := frame[0]

	m.waitersMu.Lock()
	chans := m.waiters[code]
	log.Printf("deliverFrame: code=0x%02x, len=%d, waiters for this code=%d", code, len(frame), len(chans))
	if len(chans) > 0 {
		ch := chans[0]
		m.waiters[code] = chans[1:]
		m.waitersMu.Unlock()
		log.Printf("deliverFrame: delivering code=0x%02x to waiter", code)
		select {
		case ch <- frame:
			log.Printf("deliverFrame: successfully delivered code=0x%02x to waiter", code)
		default:
			log.Printf("deliverFrame: waiter channel full, dropping code=0x%02x", code)
		}
		return
	}
	m.waitersMu.Unlock()

	// no waiter → treat as push / unsolicited
	log.Printf("deliverFrame: treating code=0x%02x as push, len=%d", code, len(frame))
	m.handlePush(frame)
}

// helper to send SyncNextMessage (CMD_SyncNextMessage = 0x0A)
func (m *InMemoryMeshcoreClient) sendSyncNextMessage() error {
	payload := []byte{models.CMD_SyncNextMessage} // 0x0A
	return m.sendFrame(payload)
}

// SendZeroHopAdvert sends a zero-hop (direct, non-routed) advertisement
func (m *InMemoryMeshcoreClient) SendZeroHopAdvert() error {
	log.Println("SendZeroHopAdvert() called")
	m.mu.Lock()
	if m.conn == nil {
		m.mu.Unlock()
		return errors.New("not connected")
	}
	m.mu.Unlock()
	
	// CMD_SendSelfAdvert (0x07) with type 0 (ZeroHop)
	payload := []byte{models.CMD_SendSelfAdvert, 0x00}
	log.Printf("--> Sending ZERO_HOP_ADVERT command: %x", payload)
	
	if err := m.sendFrame(payload); err != nil {
		return fmt.Errorf("send zero-hop advert failed: %w", err)
	}
	
	log.Println("SendZeroHopAdvert() complete")
	return nil
}

// SendFloodAdvert sends a flood-routed advertisement that propagates through the mesh
func (m *InMemoryMeshcoreClient) SendFloodAdvert() error {
	log.Println("SendFloodAdvert() called")
	m.mu.Lock()
	if m.conn == nil {
		m.mu.Unlock()
		return errors.New("not connected")
	}
	m.mu.Unlock()
	
	// CMD_SendSelfAdvert (0x07) with type 1 (Flood)
	payload := []byte{models.CMD_SendSelfAdvert, 0x01}
	log.Printf("--> Sending FLOOD_ADVERT command: %x", payload)
	
	if err := m.sendFrame(payload); err != nil {
		return fmt.Errorf("send flood advert failed: %w", err)
	}
	
	log.Println("SendFloodAdvert() complete")
	return nil
}

type IncomingContactMsg struct {
	PubKeyPrefix   [6]byte
	PathLen        byte
	TxtType        byte
	SenderUnixSecs uint32
	Text           string
}

type IncomingChannelMsg struct {
	ChannelIdx     int8
	PathLen        byte
	TxtType        byte
	SenderUnixSecs uint32
	Text           string
}

type NextMessage struct {
	Contact *IncomingContactMsg
	Channel *IncomingChannelMsg
}

// syncNextMessage mirrors JS Connection.syncNextMessage()
func (m *InMemoryMeshcoreClient) syncNextMessage(ctx context.Context) (*NextMessage, error) {
	// register waiters for the three possible responses
	log.Printf("syncNextMessage: registering waiters")
	chContact := m.addWaiter(byte(models.ResponseCodes.ContactMsgRecv))
	chChannel := m.addWaiter(byte(models.ResponseCodes.ChannelMsgRecv))
	chNoMore := m.addWaiter(byte(models.ResponseCodes.NoMoreMessages))

	// Ensure we clean up unused waiters when we return
	defer func() {
		// Remove any unused waiters from the queue
		m.removeWaiter(byte(models.ResponseCodes.ContactMsgRecv), chContact)
		m.removeWaiter(byte(models.ResponseCodes.ChannelMsgRecv), chChannel)
		m.removeWaiter(byte(models.ResponseCodes.NoMoreMessages), chNoMore)
		
		// Drain any pending messages from channels to prevent blocking
		select {
		case <-chContact:
		default:
		}
		select {
		case <-chChannel:
		default:
		}
		select {
		case <-chNoMore:
		default:
		}
	}()

	if err := m.sendSyncNextMessage(); err != nil {
		return nil, err
	}

	log.Printf("syncNextMessage: waiting for response...")
	select {
	case frame := <-chContact:
		log.Printf("syncNextMessage: received ContactMsgRecv, len=%d", len(frame))
		if len(frame) == 0 {
			return nil, nil
		}
		br := bytes.NewReader(frame[1:]) // skip code
		var pk [6]byte
		if _, err := io.ReadFull(br, pk[:]); err != nil {
			return nil, err
		}
		var pathLen [1]byte
		var txtType [1]byte
		var ts uint32
		if _, err := io.ReadFull(br, pathLen[:]); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(br, txtType[:]); err != nil {
			return nil, err
		}
		if err := binary.Read(br, binary.LittleEndian, &ts); err != nil {
			return nil, err
		}
		textBytes, err := io.ReadAll(br)
		if err != nil {
			return nil, err
		}
		return &NextMessage{
			Contact: &IncomingContactMsg{
				PubKeyPrefix:   pk,
				PathLen:        pathLen[0],
				TxtType:        txtType[0],
				SenderUnixSecs: ts,
				Text:           string(textBytes),
			},
		}, nil

	case frame := <-chChannel:
		log.Printf("syncNextMessage: received ChannelMsgRecv, len=%d", len(frame))
		if len(frame) == 0 {
			return nil, nil
		}
		br := bytes.NewReader(frame[1:])
		var idx int8
		var pathLen, txtType byte
		var ts uint32

		if err := binary.Read(br, binary.LittleEndian, &idx); err != nil {
			return nil, err
		}
		if err := binary.Read(br, binary.LittleEndian, &pathLen); err != nil {
			return nil, err
		}
		if err := binary.Read(br, binary.LittleEndian, &txtType); err != nil {
			return nil, err
		}
		if err := binary.Read(br, binary.LittleEndian, &ts); err != nil {
			return nil, err
		}
		textBytes, err := io.ReadAll(br)
		if err != nil {
			return nil, err
		}
		return &NextMessage{
			Channel: &IncomingChannelMsg{
				ChannelIdx:     idx,
				PathLen:        pathLen,
				TxtType:        txtType,
				SenderUnixSecs: ts,
				Text:           string(textBytes),
			},
		}, nil

	case <-chNoMore:
		log.Printf("syncNextMessage: received NoMoreMessages")
		return nil, nil

	case <-ctx.Done():
		log.Printf("syncNextMessage: context cancelled")
		return nil, ctx.Err()
	}
}

// getWaitingMessages mirrors JS getWaitingMessages()
func (m *InMemoryMeshcoreClient) GetWaitingMessages(ctx context.Context) ([]NextMessage, error) {
	var out []NextMessage
	for {
		msg, err := m.syncNextMessage(ctx)
		if err != nil {
			return out, err
		}
		if msg == nil {
			break
		}
		out = append(out, *msg)
	}
	return out, nil
}

func (m *InMemoryMeshcoreClient) handlePush(frame []byte) {
	if len(frame) == 0 {
		return
	}
	code := frame[0]
	switch code {
	case models.PushMsgWaiting:
		log.Printf("handlePush: MsgWaiting (0x%02x) received; fetching queued messages", code)
		// Call drainWaitingMessages synchronously so any errors and logs
		// appear inline with this push handling.
		go m.drainWaitingMessages()
	case models.PushAdvert: // 0x80
		m.sendToDebugChannel("Advert", frame)
	case models.PushPathUpdated: // 0x81
		m.sendToDebugChannel("PathUpdated", frame)
	case models.PushSendConfirmed: // 0x82
		m.handleSendConfirmed(frame)
	case byte(models.ResponseCodes.Sent): // 0x06
		m.handleSent(frame)
	case models.PushRawData: // 0x84
		m.sendToDebugChannel("RawData", frame)
	case models.PushLoginSuccess: // 0x85
		m.sendToDebugChannel("LoginSuccess", frame)
	case models.PushStatusResponse: // 0x87
		m.sendToDebugChannel("StatusResponse", frame)
	case models.PushLogRxData: // 0x88
		m.sendToDebugChannel("LogRxData", frame)
	case models.PushTraceData: // 0x89
		m.sendToDebugChannel("TraceData", frame)
	case models.PushNewAdvert: // 0x8A
		m.sendToDebugChannel("NewAdvert", frame)
	case models.PushTelemetry: // 0x8B
		m.sendToDebugChannel("Telemetry", frame)
	case models.PushBinaryResponse: // 0x8C
		m.sendToDebugChannel("BinaryResponse", frame)
	case byte(models.ResponseCodes.ChannelInfo): // 0x12
		m.sendToDebugChannel("ChannelInfo", frame)
	default:
		log.Printf("Unknown push frame code=0x%02x len=%d", code, len(frame))
		m.sendToDebugChannel(fmt.Sprintf("Unknown(0x%02x)", code), frame)
	}
}

func (m *InMemoryMeshcoreClient) handleSent(frame []byte) {
	// Sent response structure: result(1 byte) + expectedAckCrc(4 bytes) + estTimeout(4 bytes)
	if len(frame) < 9 {
		log.Printf("handleSent: frame too short: %d bytes, expected 9", len(frame))
		return
	}
	
	result := int8(frame[1])
	ackCode := binary.LittleEndian.Uint32(frame[2:6])
	estTimeout := binary.LittleEndian.Uint32(frame[6:10])
	log.Printf("handleSent: result=%d, ackCode=0x%08x, estTimeout=%dms", result, ackCode, estTimeout)
	
	// Match this ackCode with the oldest pending message (FIFO)
	m.ackCodeMu.Lock()
	var msgID string
	if len(m.recentlySentMsgs) > 0 {
		msgID = m.recentlySentMsgs[0]
		m.recentlySentMsgs = m.recentlySentMsgs[1:]
		// Register the ackCode for this message
		m.ackCodeToMsgID[ackCode] = msgID
		log.Printf("handleSent: matched ackCode=0x%08x to message %s", ackCode, msgID)
	} else {
		log.Printf("handleSent: no pending messages to match ackCode=0x%08x", ackCode)
	}
	m.ackCodeMu.Unlock()
	
	if msgID == "" {
		return
	}
	
	log.Printf("handleSent: marking message %s as sent", msgID)
	
	// Update message status in store
	if m.messageStore != nil {
		if err := m.messageStore.UpdateMessageStatus(msgID, "sent", 0); err != nil {
			log.Printf("handleSent: failed to update message status: %v", err)
		}
	}
	
	// Notify via callback
	m.mu.Lock()
	if m.onMessageStatusUpdate != nil {
		m.onMessageStatusUpdate(msgID, "sent", 0)
	}
	m.mu.Unlock()
}

func (m *InMemoryMeshcoreClient) handleSendConfirmed(frame []byte) {
	if len(frame) < 9 {
		log.Printf("handleSendConfirmed: frame too short: %d bytes", len(frame))
		return
	}
	
	ackCode := binary.LittleEndian.Uint32(frame[1:5])
	roundTrip := binary.LittleEndian.Uint32(frame[5:9])
	log.Printf("handleSendConfirmed: ackCode=0x%08x, roundTrip=%d ms", ackCode, roundTrip)
	
	// Look up message ID by ackCode
	m.ackCodeMu.Lock()
	msgID, found := m.ackCodeToMsgID[ackCode]
	if found {
		// Clean up the mapping
		delete(m.ackCodeToMsgID, ackCode)
	}
	m.ackCodeMu.Unlock()
	
	if !found {
		log.Printf("handleSendConfirmed: no message found for ackCode=0x%08x", ackCode)
		// Still send to debug channel
		m.sendToDebugChannel("SendConfirmed", frame)
		return
	}
	
	log.Printf("handleSendConfirmed: marking message %s as delivered with roundTrip=%d ms", msgID, roundTrip)
	
	// Update message status in store
	if m.messageStore != nil {
		if err := m.messageStore.UpdateMessageStatus(msgID, "delivered", roundTrip); err != nil {
			log.Printf("handleSendConfirmed: failed to update message status: %v", err)
		}
	}
	
	// Notify via callback
	m.mu.Lock()
	if m.onMessageStatusUpdate != nil {
		m.onMessageStatusUpdate(msgID, "delivered", roundTrip)
	}
	m.mu.Unlock()
	
	// Also send to debug channel
	m.sendToDebugChannel("SendConfirmed", frame)
}

func (m *InMemoryMeshcoreClient) sendToDebugChannel(pushType string, frame []byte) {
	// Decode the frame into a human-readable format
	content := fmt.Sprintf("[%s] Raw: %x", pushType, frame)
	
	// Add specific decoding based on push type
	switch frame[0] {
	case models.PushLogRxData: // 0x88
		if len(frame) >= 3 {
			snr := int8(frame[1]) / 4
			rssi := int8(frame[2])
			rawPacket := frame[3:]
			
			// Decode the packet structure to extract src/dst
			packetInfo := m.decodePacketInfo(rawPacket)
			
			if packetInfo != "" {
				content = fmt.Sprintf("[LogRxData] SNR: %.1f dB, RSSI: %d dBm, %s", float32(snr), rssi, packetInfo)
			} else {
				content = fmt.Sprintf("[LogRxData] SNR: %.1f dB, RSSI: %d dBm, Payload: %x", float32(snr), rssi, rawPacket)
			}
		}
	case models.PushAdvert: // 0x80
		if len(frame) >= 33 {
			pubKey := frame[1:33]
			contactName := m.lookupContactName(pubKey)
			if contactName != "" {
				content = fmt.Sprintf("[Advert] From: %s (%x...)", contactName, pubKey[:6])
			} else {
				content = fmt.Sprintf("[Advert] From: %x", pubKey[:6])
			}
		}
	case models.PushStatusResponse: // 0x87
		if len(frame) >= 8 {
			pubKeyPrefix := frame[2:8]
			contactName := m.lookupContactByPrefix(pubKeyPrefix)
			if contactName != "" {
				content = fmt.Sprintf("[StatusResponse] From: %s (%x), Data: %x", contactName, pubKeyPrefix, frame[8:])
			} else {
				content = fmt.Sprintf("[StatusResponse] From: %x, Data: %x", pubKeyPrefix, frame[8:])
			}
		}
	case models.PushTelemetry: // 0x8B
		if len(frame) >= 8 {
			pubKeyPrefix := frame[2:8]
			contactName := m.lookupContactByPrefix(pubKeyPrefix)
			if contactName != "" {
				content = fmt.Sprintf("[Telemetry] From: %s (%x), LPP Data: %x", contactName, pubKeyPrefix, frame[8:])
			} else {
				content = fmt.Sprintf("[Telemetry] From: %x, LPP Data: %x", pubKeyPrefix, frame[8:])
			}
		}
	case models.PushSendConfirmed: // 0x82
		if len(frame) >= 9 {
			ackCode := binary.LittleEndian.Uint32(frame[1:5])
			roundTrip := binary.LittleEndian.Uint32(frame[5:9])
			content = fmt.Sprintf("[SendConfirmed] AckCode: 0x%08x, RoundTrip: %d ms", ackCode, roundTrip)
		}
	case models.PushNewAdvert: // 0x8A
		if len(frame) >= 33 {
			pubKey := frame[1:33]
			contactName := m.lookupContactName(pubKey)
			advName := ""
			if len(frame) >= 101 { // 1 + 32 + 1 + 1 + 1 + 64 = 100, name starts at 100
				advName = string(bytes.Trim(frame[100:132], "\x00"))
			}
			if contactName != "" {
				content = fmt.Sprintf("[NewAdvert] From: %s (%x...), AdvName: %s", contactName, pubKey[:6], advName)
			} else if advName != "" {
				content = fmt.Sprintf("[NewAdvert] From: %x, AdvName: %s", pubKey[:6], advName)
			} else {
				content = fmt.Sprintf("[NewAdvert] From: %x", pubKey[:6])
			}
		}
	case byte(models.ResponseCodes.ChannelInfo): // 0x12
		if len(frame) >= 34 {
			channelIdx := int(frame[1])
			// Name is a C string (null-terminated) in a 32-byte buffer
			nameBytes := frame[2:34]
			nullIdx := bytes.IndexByte(nameBytes, 0)
			if nullIdx >= 0 {
				nameBytes = nameBytes[:nullIdx]
			}
			channelName := string(nameBytes)
			if channelName == "" {
				channelName = fmt.Sprintf("Channel %d", channelIdx)
			}
			
			// Optional: decode secret if present
			if len(frame) >= 50 { // 1 + 1 + 32 + 16 = 50
				secret := frame[34:50]
				content = fmt.Sprintf("[ChannelInfo] Index: %d, Name: %s, Secret: %x", channelIdx, channelName, secret)
			} else {
				content = fmt.Sprintf("[ChannelInfo] Index: %d, Name: %s", channelIdx, channelName)
			}
		}
	}

	msg := models.Message{
		ID:        time.Now().Format("20060102150405.000000"),
		From:      "system",
		To:        "debug",
		IsChannel: true,
		Content:   content,
		Timestamp: time.Now().UTC(),
	}

	m.mu.Lock()
	if m.messageStore != nil {
		if err := m.messageStore.AddMessage(msg); err != nil {
			log.Printf("warning: failed to persist debug message: %v", err)
		}
	}

	if m.onIncomingMessage != nil {
		log.Printf("sendToDebugChannel: pushing debug msg: %s", content)
		m.onIncomingMessage(msg)
	}
	m.mu.Unlock()
}

func (m *InMemoryMeshcoreClient) drainWaitingMessages() {
	log.Printf("drainWaitingMessages: start")
	
	// Serialize with other device I/O operations to prevent concurrent calls from interfering
	m.ioMu.Lock()
	defer m.ioMu.Unlock()
	
	// Use a background context so we rely on the node's NoMoreMessages signal
	// rather than an arbitrary timeout.
	ctx := context.Background()

	msgs, err := m.GetWaitingMessages(ctx)
	if err != nil {
		log.Printf("error draining waiting messages: %v", err)
		return
	}

	if len(msgs) == 0 {
		log.Printf("drainWaitingMessages: no waiting messages")
		return
	}

	log.Printf("drainWaitingMessages: received %d waiting message(s) from node", len(msgs))

	m.mu.Lock()
	for _, nm := range msgs {
		var msg models.Message
		if nm.Contact != nil {
			// Try to map the 6-byte pubkey prefix back to a known contact ID so
			// the frontend can match messages to the contact selected in the UI.
			prefixHex := fmt.Sprintf("%x", nm.Contact.PubKeyPrefix[:])
			fromID := prefixHex
			for _, c := range m.contacts {
				fullHex := strings.TrimPrefix(c.ID, "!")
				if strings.HasPrefix(fullHex, prefixHex) {
					fromID = c.ID
					break
				}
			}

			senderTime := time.Unix(int64(nm.Contact.SenderUnixSecs), 0).UTC()
			msg = models.Message{
				ID:         time.Now().Format("20060102150405.000000"),
				From:       fromID,
				To:         "self",
				IsChannel:  false,
				Content:    nm.Contact.Text,
				Timestamp:  senderTime,
				PathLen:    nm.Contact.PathLen,
				SenderTime: senderTime,
			}
		} else if nm.Channel != nil {
			senderTime := time.Unix(int64(nm.Channel.SenderUnixSecs), 0).UTC()
			msg = models.Message{
				ID:         time.Now().Format("20060102150405.000000"),
				From:       fmt.Sprintf("ch%d", nm.Channel.ChannelIdx),
				To:         "channel",
				IsChannel:  true,
				Content:    nm.Channel.Text,
				Timestamp:  senderTime,
				PathLen:    nm.Channel.PathLen,
				SenderTime: senderTime,
			}
		} else {
			continue
		}

		if m.messageStore != nil {
			if err := m.messageStore.AddMessage(msg); err != nil {
				log.Printf("warning: failed to persist incoming message: %v", err)
			}
		}

		if m.onIncomingMessage != nil {
			log.Printf("drainWaitingMessages: invoking onIncomingMessage, from=%s to=%s content=%q",
				msg.From, msg.To, msg.Content)
			m.onIncomingMessage(msg)
		}
	}
	m.mu.Unlock()
	log.Printf("drainWaitingMessages: Finished processing %d messages", len(msgs))
}

func (m *InMemoryMeshcoreClient) lookupContactName(pubKey []byte) string {
	pubKeyHex := hex.EncodeToString(pubKey)
	for _, c := range m.contacts {
		if strings.HasSuffix(c.ID, pubKeyHex) {
			return c.Name
		}
	}
	return ""
}

func (m *InMemoryMeshcoreClient) lookupContactByPrefix(prefix []byte) string {
	prefixHex := hex.EncodeToString(prefix)
	for _, c := range m.contacts {
		fullHex := strings.TrimPrefix(c.ID, "!")
		if strings.HasPrefix(fullHex, prefixHex) {
			return c.Name
		}
	}
	return ""
}

func (m *InMemoryMeshcoreClient) decodePacketInfo(packet []byte) string {
	if len(packet) < 2 {
		return ""
	}

	// Packet structure (simplified):
	// byte 0: flags/control
	// byte 1: payload_type (lower bits) + route info
	
	payloadType := packet[1] & 0x0F
	
	// Check if this is an ACK packet
	if payloadType == 0x06 { // PAYLOAD_TYPE_ACK
		if len(packet) >= 6 {
			ackCRC := binary.LittleEndian.Uint32(packet[2:6])
			return fmt.Sprintf("Type: ACK, CRC: 0x%08x", ackCRC)
		}
		return "Type: ACK"
	}
	
	// Check if this is a data packet with src/dst hashes
	if len(packet) >= 4 && (payloadType == 0x01 || payloadType == 0x02 || payloadType == 0x03 || payloadType == 0x04) {
		// PAYLOAD_TYPE_PATH (0x01), REQ (0x02), RESPONSE (0x03), TXT_MSG (0x04)
		// These have: dest_hash (1 byte), src_hash (1 byte), then encrypted data
		
		// Skip to payload data (after packet header)
		// Simplified: assume payload starts at offset 2
		if len(packet) > 3 {
			destHash := packet[2]
			srcHash := packet[3]
			
			srcName := m.lookupContactByPrefix([]byte{srcHash})
			dstName := m.lookupContactByPrefix([]byte{destHash})
			
			typeNames := map[byte]string{
				0x01: "PATH",
				0x02: "REQ",
				0x03: "RESPONSE",
				0x04: "TXT_MSG",
			}
			typeName := typeNames[payloadType]
			
			if srcName != "" && dstName != "" {
				return fmt.Sprintf("Type: %s, From: %s (hash:%02x), To: %s (hash:%02x)", typeName, srcName, srcHash, dstName, destHash)
			} else if srcName != "" {
				return fmt.Sprintf("Type: %s, From: %s (hash:%02x), To: hash:%02x", typeName, srcName, srcHash, destHash)
			} else if dstName != "" {
				return fmt.Sprintf("Type: %s, From: hash:%02x, To: %s (hash:%02x)", typeName, srcHash, dstName, destHash)
			} else {
				return fmt.Sprintf("Type: %s, From: hash:%02x, To: hash:%02x", typeName, srcHash, destHash)
			}
		}
	} else if payloadType == 0x05 { // PAYLOAD_TYPE_ANON_REQ
		if len(packet) >= 35 {
			destHash := packet[2]
			senderPubKey := packet[3:35]
			dstName := m.lookupContactByPrefix([]byte{destHash})
			senderName := m.lookupContactName(senderPubKey)
			
			if senderName != "" && dstName != "" {
				return fmt.Sprintf("Type: ANON_REQ, From: %s (%x...), To: %s (hash:%02x)", senderName, senderPubKey[:6], dstName, destHash)
			} else if senderName != "" {
				return fmt.Sprintf("Type: ANON_REQ, From: %s (%x...), To: hash:%02x", senderName, senderPubKey[:6], destHash)
			} else if dstName != "" {
				return fmt.Sprintf("Type: ANON_REQ, From: %x, To: %s (hash:%02x)", senderPubKey[:6], dstName, destHash)
			} else {
				return fmt.Sprintf("Type: ANON_REQ, From: %x, To: hash:%02x", senderPubKey[:6], destHash)
			}
		}
	} else if payloadType == 0x07 || payloadType == 0x08 {
		// PAYLOAD_TYPE_GRP_DATA (0x07) or GRP_TXT (0x08)
		if len(packet) > 2 {
			channelHash := packet[2]
			return fmt.Sprintf("Type: 0x%02x (Group), Channel hash: %02x", payloadType, channelHash)
		}
	} else if payloadType == 0x09 { 
		// PAYLOAD_TYPE_ADVERT (0x09)
		if len(packet) > 34 {
			pubKey := packet[2:34]
			contactName := m.lookupContactName(pubKey)
			if contactName != "" {
				return fmt.Sprintf("Type: Advert, From: %s (%x...)", contactName, pubKey[:6])
			}
			return fmt.Sprintf("Type: Advert, From: %x", pubKey[:6])
		}
	}
	
	if len(packet) > 20 {
		return fmt.Sprintf("Type: 0x%02x, Payload: %x...", payloadType, packet[:20])
	}
	return fmt.Sprintf("Type: 0x%02x, Payload: %x", payloadType, packet)
}

func performMeshcoreHandshake(conn net.Conn) (models.DeviceInfo, error) {
	log.Println("performing meshcore handshake...")

	sendFrame := func(payload []byte) error {
		frame := make([]byte, 3+len(payload))
		frame[0] = 0x3c
		binary.LittleEndian.PutUint16(frame[1:3], uint16(len(payload)))
		copy(frame[3:], payload)
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err := conn.Write(frame)
		return err
	}

	readFrame := func() ([]byte, error) {
		head := make([]byte, 3)
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(conn, head); err != nil {
			return nil, err
		}
		if head[0] != 0x3e {
			return nil, fmt.Errorf("unexpected frame prefix: 0x%02x", head[0])
		}
		sz := binary.LittleEndian.Uint16(head[1:3])
		if sz > 4096 {
			return nil, fmt.Errorf("frame too large: %d", sz)
		}
		buf := make([]byte, sz)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return nil, err
		}
		log.Printf("<-- RECV FRAME: len=%d, data=%x", sz, buf)
		return buf, nil
	}

	// Match meshcore.js AppStart layout: cmd=0x01, appVer=1, 6x reserved=0x00, then app name.
	appstartPayload := []byte{
		0x01,                               // CMD_AppStart
		0x01,                               // appVer = 1
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // 6 reserved bytes
		0x6d, 0x63, 0x63, 0x6c, 0x69, // "mccli" as app name
	}
	log.Printf("--> Sending APPSTART command: %x", appstartPayload)
	if err := sendFrame(appstartPayload); err != nil {
		return models.DeviceInfo{}, fmt.Errorf("send APPSTART failed: %w", err)
	}

	resp, err := readFrame()
	if err != nil {
		return models.DeviceInfo{}, fmt.Errorf("read response for APPSTART failed: %w", err)
	}
	if len(resp) == 0 || resp[0] != 0x05 { // SELF_INFO
		return models.DeviceInfo{}, fmt.Errorf("expected SELF_INFO (0x05) but got type 0x%02x", resp[0])
	}
	log.Println("... Received SELF_INFO packet")

	// Parse SELF_INFO to extract public key (needed for message storage)
	selfInfo, err := parseSelfInfo(resp)
	if err != nil {
		log.Printf("Warning: failed to parse SELF_INFO: %v", err)
	}

	// Match meshcore.js DeviceQuery: cmd=0x16, appTargetVer=1
	getDeviceInfoCmd := []byte{0x16, 0x01}
	log.Printf("--> Sending DEVICE_INFO command: %x", getDeviceInfoCmd)
	if err := sendFrame(getDeviceInfoCmd); err != nil {
		return models.DeviceInfo{}, fmt.Errorf("send DEVICE_INFO failed: %w", err)
	}

	resp, err = readFrame()
	if err != nil {
		return models.DeviceInfo{}, fmt.Errorf("read response for DEVICE_INFO failed: %w", err)
	}

	info, err := parseDeviceInfo(resp)
	if err != nil {
		return models.DeviceInfo{}, fmt.Errorf("could not parse device info: %w", err)
	}

	// Add public key and user name from SELF_INFO if we got it
	if selfInfo != nil {
		info.PublicKey = selfInfo.PublicKey
		info.UserName = selfInfo.UserName
	}

	log.Println("performMeshcoreHandshake complete")
	return info, nil
}

func parseDeviceInfo(data []byte) (models.DeviceInfo, error) {
	info := models.DeviceInfo{}
	if len(data) < 2 {
		return info, errors.New("DEVICE_INFO packet too short")
	}
	if data[0] != 13 {
		return info, fmt.Errorf("unexpected packet type: %d", data[0])
	}

	fwVer := int(data[1])
	info.FirmwareVersion = fmt.Sprintf("fw-%d", fwVer)

	if fwVer < 3 {
		return info, nil
	}

	if len(data) >= 3 {
		info.MaxContacts = int(data[2]) * 2
	}
	if len(data) >= 4 {
		info.MaxChannels = int(data[3])
	}

	if len(data) < 80 {
		return info, nil
	}

	idx := 2
	idx += 1
	idx += 1
	idx += 4

	info.FirmwareBuild = strings.TrimRight(string(data[idx:idx+12]), "\x00")
	idx += 12

	info.Model = strings.TrimRight(string(data[idx:idx+40]), "\x00")
	idx += 40

	verBytes := data[idx:]
	verStr := strings.TrimRight(string(verBytes), "\x00")
	verStr = strings.TrimSpace(verStr)

	if verStr != "" {
		info.FirmwareVersion = verStr
	}

	log.Println("[*] ParseDeviceInfo complete")

	return info, nil
}

type SelfInfo struct {
	PublicKey string
	UserName  string
}

func parseSelfInfo(data []byte) (*SelfInfo, error) {
	if len(data) < 37 { // 1 byte type + 1 byte txPower + 1 byte maxTxPower + 32 byte pubkey + ...
		return nil, errors.New("SELF_INFO packet too short")
	}
	if data[0] != 0x05 {
		return nil, fmt.Errorf("unexpected packet type: 0x%02x", data[0])
	}

	// SELF_INFO structure (from connection.js onSelfInfoResponse):
	// byte 0: type (0x05)
	// byte 1: txPower
	// byte 2: maxTxPower
	// bytes 3-34: publicKey (32 bytes)
	// bytes 35-38: advLat (int32 LE)
	// bytes 39-42: advLon (int32 LE)
	// bytes 43-45: reserved (3 bytes)
	// byte 46: manualAddContacts
	// bytes 47-50: radioFreq (uint32 LE)
	// bytes 51-54: radioBw (uint32 LE)
	// byte 55: radioSf
	// byte 56: radioCr
	// bytes 57+: name (null-terminated string)

	publicKey := data[3:35]
	publicKeyHex := hex.EncodeToString(publicKey)

	// Extract user name if present
	userName := ""
	if len(data) > 57 {
		nameBytes := data[57:]
		// Find null terminator
		nullIdx := bytes.IndexByte(nameBytes, 0)
		if nullIdx >= 0 {
			nameBytes = nameBytes[:nullIdx]
		}
		userName = string(nameBytes)
	}

	return &SelfInfo{
		PublicKey: publicKeyHex,
		UserName:  userName,
	}, nil
}

func getNodePublicKeyHex(info models.DeviceInfo) string {
	// Use public key if available, otherwise fall back to a combination of model + firmware
	if info.PublicKey != "" {
		return info.PublicKey
	}
	// Fallback identifier (shouldn't happen if handshake works)
	return fmt.Sprintf("%s_%s", info.Model, info.FirmwareVersion)
}
