# MeshCore Web GUI - Development Notes

## Project Overview
A web-based GUI for MeshCore mesh networking devices (only via WiFi at the moment), built with Go backend and vanilla JavaScript frontend.

## Architecture

### Backend (Go)
- **main.go**: HTTP server, WebSocket hub, API endpoints
- **app/client/client.go**: MeshCore protocol implementation, handles serial communication with node
- **app/models/**: Data models and protocol constants
- **app/storage/**: Message persistence (JSON files per node)

### Frontend
- **static/index.html**: Main UI structure with modals
- **static/app.js**: WebSocket client, state management, UI interactions
- **static/styles.css**: Dark theme styling

## Key Features Implemented

### 1. Node Connection
- Connect to MeshCore devices via TCP (host:port)
- Auto-detect device info (model, firmware version, capacity)
- Persistent connection with WebSocket for real-time updates
- Message persistence per node (stored in `data/messages_<hash>.json`)

### 2. Contacts & Channels
- Display contacts discovered on the mesh network
- Favorites system with star button (persisted to backend storage)
- Support for up to 8 channels (indices 0-7)
- Add channel with index, name, and secret (16-byte hex)
- Clear/delete channel slots
- Virtual "Debug" channel for network monitoring (limited to 100 messages)

### 3. Messaging
- Send messages to contacts (node-to-node)
- Send messages to channels (broadcast)
- Message history displayed per conversation
- Click contact/channel to view messages
- Visual selection highlighting
- Message status indicators (✓ sent, ✓✓ delivered with round-trip time)
- Character counter (150 char limit)
- Enter key sends message
- Routing metadata: hop count and latency displayed for received messages
- Timestamps displayed in UTC (24-hour format)

### 4. Channel Management
- **Public channels**: Use hashtag prefix (e.g., `#NornIron`)
  - Require explicit shared secret
  - Default public channel secret: `8b3387e9c5cdea6ac9e5edbaa115cd72`
- **Private channels**: Random secret generation
- Channel configuration synchronized across devices
- Delete button (×) to clear channel slots

### 5. Debug Channel
- Virtual channel that captures all unhandled network traffic
- Limited to 100 most recent messages (auto-trims older debug messages)
- Decodes various packet types:
  - LogRxData (0x88): SNR, RSSI, packet info
  - Advert (0x80): Node advertisements
  - StatusResponse (0x87)
  - Telemetry (0x8B)
  - SendConfirmed (0x82)
  - NewAdvert (0x8A)
  - ChannelInfo (0x12): Channel configuration details
- Shows source/destination contact names when available
- Packet type decoding (ACK, PATH, REQ, RESPONSE, TXT_MSG, etc.)

### 6. Notification System
- Unread message indicators (red text, bold font)
- Tracks unread count per contact/channel
- Marks as read when conversation is opened
- Ignores self and system messages

### 7. Channel Mentions
- Mention users with `@[USERNAME]` syntax in channel messages
- Highlighted messages (green background) when you're mentioned
- Double-click message to insert mention in input box
- Works only for channel messages

### 8. Network Operations
- Send Zero-Hop Advert: Broadcast presence to direct neighbors only
- Send Flood Advert: Broadcast presence throughout the mesh network
- Buttons in topbar for quick access

## Protocol Details

### MeshCore Serial Protocol
- **Framing**: `<length><checksum><sequence><payload>`
- **Commands** (CMD_*): 0x01-0x50 range
- **Responses** (RESP_*): 0x00-0x14 range
- **Push codes** (PUSH_*): 0x80-0x8E range

### Key Commands
- `CMD_AppStart (0x01)`: Handshake with node
- `CMD_GetContacts (0x04)`: Retrieve contact list
- `CMD_GetChannel (0x1F)`: Get channel info by index
- `CMD_SetChannel (0x20)`: Configure channel (index, name, 32-byte name + 16-byte secret)
- `CMD_SendTxtMsg (0x02)`: Send contact message
- `CMD_SendChannelTxtMsg (0x03)`: Send channel message
- `CMD_SyncNextMessage (0x0A)`: Poll for incoming messages
- `CMD_SendSelfAdvert (0x07)`: Send advertisement (type 0=ZeroHop, type 1=Flood)

### Response Handling
- Uses waiter system to avoid race conditions
- `0x00` = OK response
- `0x01` = Error response (with error code in byte 2)

## Known Issues & Limitations

### 1. SetChannel Timeout Display
- Channel operations succeed but show timeout in UI
- Root cause: OK response (0x00) arrives asynchronously
- **Fixed**: Now uses waiter system instead of direct readFrame()

### 2. Channel Name Parsing
- Channel names are C strings (null-terminated, 32-byte buffer)
- Must properly find null terminator to avoid reading into secret bytes
- **Fixed**: Uses IndexByte to find null terminator

### 3. Message Routing
- Messages appear on correct channels when both devices have matching configuration
- Channel index, name, and secret must match exactly on all participating devices
- Public channels require out-of-band secret sharing

### 4. GetChannel Loop
- Limited to indices 0-3 (MAX_GROUP_CHANNELS = 4 on most devices)
- Empty/unconfigured slots return errors and are skipped

## File Structure
```
go-meshcore-webgui/
├── main.go                     # HTTP server, WebSocket hub, API routes
├── app/
│   ├── client/
│   │   └── client.go          # MeshCore protocol client
│   ├── models/
│   │   ├── models.go          # Data structures
│   │   └── constants.go       # Protocol constants
│   └── storage/
│       └── storage.go         # Message persistence
├── static/
│   ├── index.html             # UI layout
│   ├── app.js                 # Frontend logic
│   └── styles.css             # Styling
├── data/                      # Message storage (gitignored)
│   └── messages_*.json
├── MeshCore/                  # MeshCore firmware source (submodule)
└── NOTES.md                   # This file
```

## API Endpoints

### REST API
- `POST /api/connect`: Connect to MeshCore node (host, port)
- `POST /api/disconnect`: Disconnect from node
- `POST /api/add-channel`: Add/configure channel (channelIdx, name, secret)
- `POST /api/clear-channel`: Clear channel slot (channelIdx)

### WebSocket Messages
#### Client → Server
- `subscribe_state`: Request full state (contacts, channels, messages, favorites)
- `send_message`: Send message (targetId, targetType, content)
- `toggle_favorite`: Toggle favorite status for a contact
- `send_zero_hop_advert`: Send zero-hop advertisement
- `send_flood_advert`: Send flood-routed advertisement

#### Server → Client
- `state`: Full state snapshot (contacts, channels, messages, favorites)
- `message`: New incoming message
- `message_status`: Message status update (sent/delivered with round-trip time)
- `favorites_updated`: Updated favorites list
- `node_status`: Connection status update
- `advert_sent`: Advertisement sent confirmation
- `error`: Error message

## Configuration

### Environment Variables
- `MESHCORE_GUI_ADDR`: Server bind address (default: `127.0.0.1:8080`)

### Node Connection
- Default: `127.0.0.1:5000` (local serial bridge)
- Can connect to remote nodes over network

## Development Workflow

1. **Start Go server**: `go run main.go`
2. **Open browser**: `http://127.0.0.1:8080`
3. **Connect to node**: Click "Connect" → Enter host/port
4. **Make changes**: Edit files
5. **Reload**: Refresh browser (Ctrl+Shift+R for hard reload)
   - Cache busting: Update `?v=X` in index.html script tag

## Testing Checklist

- [ ] Connect to node
- [ ] View contacts
- [ ] View channels
- [ ] Add new channel (public and private)
- [ ] Send message to contact
- [ ] Send message to channel
- [ ] Clear channel
- [ ] Unread notifications appear
- [ ] Unread cleared on selection
- [ ] Debug channel shows network activity
- [ ] Message persistence across reconnect
- [ ] Multiple browser tabs sync via WebSocket

## Future Enhancements

### High Priority
- ~~Message delivery confirmation/receipts~~
- ~~Contact management (add, edit, delete)~~
- Export/import contacts and channels
- Better error handling and user feedback
- Connection status reconnection logic

### Medium Priority
- Message search/filter
- Contact/channel grouping
- GPS position display
- Signal strength indicators
- ~~Message timestamps formatting~~

### Low Priority
- Audio notifications for new messages
- Desktop notifications API
- Message encryption status display
- Network topology visualization
- Advanced debug packet analysis

## Troubleshooting

### "Unknown push frame code=0x00"
- This is informational, not an error
- Indicates OK response being handled asynchronously
- Fixed in SetChannel by using waiter system

### Messages going to wrong channel
- Verify channel configuration matches on all devices
- Check channel index, name, and secret are identical
- Use GetChannel to verify configuration

### Browser not updating
- Hard refresh: Ctrl+Shift+R (Windows) or Cmd+Shift+R (Mac)
- Clear browser cache
- Check cache busting version in index.html

### Node connection timeout
- Verify node is running and accessible
- Check firewall settings
- Ensure correct host:port combination

## References
- MeshCore firmware (https://github.com/meshcore-dev/MeshCore)
- MeshCore.js (https://www.npmjs.com/package/@liamcottle/meshcore.js)
