package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go-meshcore-webgui/app/models"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// MessageStore handles persistent storage of messages and favorites per node
type MessageStore struct {
	mu              sync.RWMutex
	messages        []models.Message
	favorites       []string // List of favorite contact IDs
	messagesPath    string
	favoritesPath   string
}

// NewMessageStore creates a new message store for a specific node
// nodeID should be a unique identifier for the node (e.g. public key hex)
func NewMessageStore(nodeID string) (*MessageStore, error) {
	// Create a data directory if it doesn't exist
	dataDir := "data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	// Create a safe filename from the nodeID (hash it to avoid issues with special chars)
	hash := sha256.Sum256([]byte(nodeID))
	safeFilename := hex.EncodeToString(hash[:])[:16] + ".json"
	messagesPath := filepath.Join(dataDir, "messages_"+safeFilename)
	favoritesPath := filepath.Join(dataDir, "favorites_"+safeFilename)

	store := &MessageStore{
		messages:      []models.Message{},
		favorites:     []string{},
		messagesPath:  messagesPath,
		favoritesPath: favoritesPath,
	}

	// Load existing messages if the file exists
	if err := store.loadMessages(); err != nil {
		log.Printf("Warning: failed to load messages from %s: %v", messagesPath, err)
		// Don't fail if we can't load - just start fresh
	} else {
		log.Printf("Loaded %d messages from %s", len(store.messages), messagesPath)
	}

	// Load existing favorites if the file exists
	if err := store.loadFavorites(); err != nil {
		log.Printf("Warning: failed to load favorites from %s: %v", favoritesPath, err)
		// Don't fail if we can't load - just start fresh
	} else {
		log.Printf("Loaded %d favorites from %s", len(store.favorites), favoritesPath)
	}

	return store, nil
}

// loadMessages reads messages from the file
func (s *MessageStore) loadMessages() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.messagesPath)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist yet - this is fine
			return nil
		}
		return err
	}

	if len(data) == 0 {
		return nil
	}

	if err := json.Unmarshal(data, &s.messages); err != nil {
		return fmt.Errorf("failed to parse messages file: %w", err)
	}

	return nil
}

// loadFavorites reads favorites from the file
func (s *MessageStore) loadFavorites() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.favoritesPath)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist yet - this is fine
			return nil
		}
		return err
	}

	if len(data) == 0 {
		return nil
	}

	if err := json.Unmarshal(data, &s.favorites); err != nil {
		return fmt.Errorf("failed to parse favorites file: %w", err)
	}

	return nil
}

// saveMessages writes messages to the file
func (s *MessageStore) saveMessages() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.messages, "", "  ")
	s.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("failed to marshal messages: %w", err)
	}

	// Write to a temp file first, then rename (atomic operation)
	tempPath := s.messagesPath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tempPath, s.messagesPath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// saveFavorites writes favorites to the file
func (s *MessageStore) saveFavorites() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.favorites, "", "  ")
	s.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("failed to marshal favorites: %w", err)
	}

	// Write to a temp file first, then rename (atomic operation)
	tempPath := s.favoritesPath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tempPath, s.favoritesPath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// AddMessage adds a message and saves to disk
func (s *MessageStore) AddMessage(msg models.Message) error {
	s.mu.Lock()
	s.messages = append(s.messages, msg)
	
	// For debug channel, limit to 100 most recent debug messages
	if msg.To == "debug" {
		// Count debug messages
		debugCount := 0
		for _, m := range s.messages {
			if m.To == "debug" {
				debugCount++
			}
		}
		
		// If we have more than 100 debug messages, remove oldest debug messages
		if debugCount > 100 {
			excessCount := debugCount - 100
			newMessages := make([]models.Message, 0, len(s.messages))
			removedCount := 0
			
			for _, m := range s.messages {
				// Remove oldest debug messages until we've removed enough
				if m.To == "debug" && removedCount < excessCount {
					removedCount++
					continue
				}
				newMessages = append(newMessages, m)
			}
			
			s.messages = newMessages
		}
	}
	
	s.mu.Unlock()

	return s.saveMessages()
}

// GetMessages returns all messages (read-only copy)
func (s *MessageStore) GetMessages() []models.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	// Return a copy to avoid race conditions
	result := make([]models.Message, len(s.messages))
	copy(result, s.messages)
	return result
}

// UpdateMessageStatus updates the status and roundTripMs of a message
func (s *MessageStore) UpdateMessageStatus(messageID string, status string, roundTripMs uint32) error {
	s.mu.Lock()
	for i := range s.messages {
		if s.messages[i].ID == messageID {
			s.messages[i].Status = status
			if roundTripMs > 0 {
				s.messages[i].RoundTripMs = roundTripMs
			}
			s.mu.Unlock()
			return s.saveMessages()
		}
	}
	s.mu.Unlock()
	return fmt.Errorf("message with ID %s not found", messageID)
}

// RemoveChannelMessages removes all messages for a specific channel
func (s *MessageStore) RemoveChannelMessages(channelID string) error {
	s.mu.Lock()
	filtered := make([]models.Message, 0)
	for _, msg := range s.messages {
		// Keep message if it's not for this channel
		if !msg.IsChannel || (msg.From != channelID && msg.To != channelID) {
			filtered = append(filtered, msg)
		}
	}
	s.messages = filtered
	s.mu.Unlock()

	return s.saveMessages()
}

// Clear removes all messages and deletes the files
func (s *MessageStore) Clear() error {
	s.mu.Lock()
	s.messages = []models.Message{}
	s.favorites = []string{}
	s.mu.Unlock()

	// Remove the messages file
	if err := os.Remove(s.messagesPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove messages file: %w", err)
	}

	// Remove the favorites file
	if err := os.Remove(s.favoritesPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove favorites file: %w", err)
	}

	return nil
}

// GetFavorites returns all favorite contact IDs
func (s *MessageStore) GetFavorites() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	// Return a copy to avoid race conditions
	result := make([]string, len(s.favorites))
	copy(result, s.favorites)
	return result
}

// AddFavorite adds a contact ID to favorites
func (s *MessageStore) AddFavorite(contactID string) error {
	s.mu.Lock()
	// Check if already exists
	for _, id := range s.favorites {
		if id == contactID {
			s.mu.Unlock()
			return nil // Already a favorite
		}
	}
	s.favorites = append(s.favorites, contactID)
	s.mu.Unlock()

	return s.saveFavorites()
}

// RemoveFavorite removes a contact ID from favorites
func (s *MessageStore) RemoveFavorite(contactID string) error {
	s.mu.Lock()
	filtered := make([]string, 0)
	for _, id := range s.favorites {
		if id != contactID {
			filtered = append(filtered, id)
		}
	}
	s.favorites = filtered
	s.mu.Unlock()

	return s.saveFavorites()
}
