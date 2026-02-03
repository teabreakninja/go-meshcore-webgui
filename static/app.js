(function () {
  const statusEl = document.getElementById("connection-status");
  const nodeModelEl = document.getElementById("node-model");
  const nodeFwBuildEl = document.getElementById("node-fw-build");
  const nodeCapacityEl = document.getElementById("node-capacity");

  const contactsListEl = document.getElementById("contacts-list");
  const channelsListEl = document.getElementById("channels-list");
  const messagesEl = document.getElementById("messages");
  const targetTypeEl = document.getElementById("target-type");
  const targetIdEl = document.getElementById("target-id");
  const messageInputEl = document.getElementById("message-input");
  const sendBtnEl = document.getElementById("send-btn");
  const charCounterEl = document.getElementById("char-counter");

  const connectBtnEl = document.getElementById("node-connect-btn");
  const modalEl = document.getElementById("connect-modal");
  const nodeHostEl = document.getElementById("node-host");
  const nodePortEl = document.getElementById("node-port");
  const connectConfirmEl = document.getElementById("connect-confirm-btn");
  const connectCancelEl = document.getElementById("connect-cancel-btn");

  const addChannelBtnEl = document.getElementById("add-channel-btn");
  const addChannelModalEl = document.getElementById("add-channel-modal");
  const addChannelCancelEl = document.getElementById("add-channel-cancel-btn");
  const addChannelConfirmEl = document.getElementById("add-channel-confirm-btn");
  const channelIndexEl = document.getElementById("channel-index");
  const channelNameEl = document.getElementById("channel-name");
  const channelSecretEl = document.getElementById("channel-secret");

  const sendZeroHopAdvertBtn = document.getElementById("send-zero-hop-advert-btn");
  const sendFloodAdvertBtn = document.getElementById("send-flood-advert-btn");

  let ws = null;
  let nodeConnected = false;
  let nodeAddress = "";
  let nodeFirmware = "";
  let userName = ""; // User's advertising name
  let resolveNodeConnection = null;
  // NEW: store all messages and the current selected target
  let allMessages = [];
  let selectedTarget = { type: null, id: null }; // type: "contact" | "channel" | null
  let unreadCounts = {}; // Track unread messages per contact/channel: { "!abc123": 5, "ch1": 2 }
  let allContacts = []; // Cache of contacts
  let allChannels = []; // Cache of channels
  let favoriteContacts = new Set(); // Set of favorite contact IDs

  // Toggle favorite status
  function toggleFavorite(contactId) {
    // Send toggle request to backend
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      alert("Not connected");
      return;
    }
    ws.send(JSON.stringify({ type: "toggle_favorite", targetId: contactId }));
  }

  function setStatus(connected) {
    console.log(`[setStatus] called with connected: ${connected}`);
    if (connected) {
      statusEl.textContent = "Connected";
      statusEl.classList.remove("status-disconnected");
      statusEl.classList.add("status-connected");
      sendBtnEl.disabled = false;
    } else {
      statusEl.textContent = "Disconnected";
      statusEl.classList.add("status-disconnected");
      statusEl.classList.remove("status-connected");
      sendBtnEl.disabled = true;
    }
  }

  function updateNodeStatus(payload) {
    if (!payload) return;
    nodeConnected = !!payload.connected;
    console.log(`[updateNodeStatus] called with payload:`, payload, `| Setting nodeConnected to: ${nodeConnected}`);

    const deviceInfo = payload.deviceInfo || {};
    nodeAddress = deviceInfo.model ? `${deviceInfo.model} (${deviceInfo.firmwareVersion})` : "";
    nodeFirmware = deviceInfo.firmwareVersion || "";
    userName = deviceInfo.userName || "";

    // Update the new UI elements
    nodeModelEl.textContent = deviceInfo.model || "-";
    nodeFwBuildEl.textContent = nodeFirmware + " (" +deviceInfo.firmwareBuild  +")"|| "-";
    if (nodeConnected) {
      nodeCapacityEl.textContent = `Contacts: ${deviceInfo.maxContacts || 0}, Channels: ${deviceInfo.maxChannels || 0}`;
    } else {
      console.log("[updateNodeStatus] Node is disconnected. Clearing capacity and contacts list.");
      nodeCapacityEl.textContent = "-";
      contactsListEl.innerHTML = "";
      channelsListEl.innerHTML = "";
      messagesEl.innerHTML = "";
    }

    connectBtnEl.textContent = nodeConnected ? "Disconnect" : "Connect";
    if (nodeConnected) {
      // ensure the connect modal is dismissed once a successful connection is established
      closeConnectModal();
      console.log(`[updateNodeStatus] Successfully connected to MeshCore node at ${nodeAddress} (fw: ${nodeFirmware})`);
      if (resolveNodeConnection) {
        resolveNodeConnection(true);
        resolveNodeConnection = null;
      }
      // Now that we are connected to the node, request the state
      ws.send(JSON.stringify({ type: "subscribe_state" }));
    }
    setStatus(nodeConnected);
  }

  function openConnectModal() {
    modalEl.classList.remove("hidden");
    if (!nodeHostEl.value) {
      nodeHostEl.value = "127.0.0.1";
    }
    if (!nodePortEl.value) {
      nodePortEl.value = "5000";
    }
    nodeHostEl.focus();
  }

  function closeConnectModal() {
    console.log("[closeConnectModal] called. Hiding modal elements.");
    modalEl.classList.add("hidden");
  }

  async function connectToNode(host, port) {
    console.log(`[connectToNode] called with host: ${host}, port: ${port}`);
    return new Promise(async (resolve) => {
      resolveNodeConnection = resolve;
      try {
        const res = await fetch("/api/connect", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ host, port }),
        });
        console.log("[connectToNode] fetch to /api/connect completed with status:", res.status);
        const data = await res.json();
        console.log("[connectToNode] received data from /api/connect:", data);
        if (!res.ok || !data.success) {
          alert(data.error || "Failed to connect to node");
          if (resolveNodeConnection) {
            console.log("[connectToNode] API call failed. Resolving connection promise with false.");
            resolveNodeConnection(false);
            resolveNodeConnection = null;
          }
        } else {
          console.log("[connectToNode] API call acknowledged. Awaiting WebSocket 'node_status' message to confirm connection.");
        }
      } catch (e) {
        console.error("connect error", e);
        console.log("[connectToNode] fetch to /api/connect threw an exception. Resolving connection promise with false.");
        alert("Failed to connect to backend");
      }
    });
  }

  async function disconnectFromNode() {
    try {
      const res = await fetch("/api/disconnect", { method: "POST" });
      const data = await res.json();
      updateNodeStatus({ connected: data.connected, address: data.address });
    } catch (e) {
      console.error("disconnect error", e);
      alert("Failed to disconnect from backend");
    }
  }

  function connectWs() {
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const url = proto + "://" + window.location.host + "/ws";
    ws = new WebSocket(url);

    ws.onopen = () => {
      // On initial connection, the server will send us the current node_status
    };

    ws.onclose = () => {
      setStatus(false);
      // basic retry; you can make this exponential backoff if desired
      setTimeout(connectWs, 2000);
    };

    ws.onerror = () => {
      setStatus(false);
    };

    ws.onmessage = (event) => {
      try {
        console.log("[ws.onmessage] received message:", event.data);
        const msg = JSON.parse(event.data);
        handleMessage(msg);
      } catch (e) {
        console.error("invalid message", e);
      }
    };
  }

  function handleMessage(msg) {
    switch (msg.type) {
      case "state":
        console.log("[handleMessage] received 'state':", msg.payload);
        renderState(msg.payload);
        break;
      case "favorites_updated":
        console.log("[handleMessage] received 'favorites_updated':", msg.payload);
        // Update favorites from backend
        favoriteContacts = new Set(msg.payload || []);
        renderContacts(allContacts);
        break;
      case "message":
        console.log("[handleMessage] received 'message':", msg.payload);
        const m = msg.payload.message;
        // Store in global list
        allMessages.push(m);
        
        // Track unread messages (skip messages from self and system)
        if (m.from !== "self" && m.from !== "system") {
          const targetId = m.isChannel ? m.from : m.from;
          if (!selectedTarget.id || selectedTarget.id !== targetId) {
            // Message is not for the currently selected conversation
            unreadCounts[targetId] = (unreadCounts[targetId] || 0) + 1;
            updateUnreadIndicators();
          }
          
          // Update LastSeen for this contact and re-render
          if (!m.isChannel) {
            const contact = allContacts.find(c => c.id === m.from);
            if (contact) {
              contact.lastSeen = new Date().toISOString();
              renderContacts(allContacts);
            }
          }
        }
        
        // Only render if it matches the current selection
        if (messageMatchesSelection(m)) {
          appendMessage(m);
        }
        break;
      case "node_status":
        console.log("[handleMessage] received 'node_status':", msg.payload);
        updateNodeStatus(msg.payload);
        // if a connection attempt was in progress and failed, resolve the promise
        if (!msg.payload.connected && resolveNodeConnection) {
          console.log("[handleMessage] node_status is 'disconnected'. Resolving connection promise with false.");
          resolveNodeConnection(false);
          resolveNodeConnection = null;
        }
        break;
      case "message_status":
        console.log("[handleMessage] received 'message_status':", msg.payload);
        updateMessageStatus(msg.payload.messageId, msg.payload.status, msg.payload.roundTripMs);
        break;
      case "error":
        alert(msg.error || "Unknown error from server");
        break;
      default:
        console.warn("Unknown message type", msg);
    }
  }

  function updateMessageStatus(messageId, status, roundTripMs) {
    // Update in allMessages array
    const msg = allMessages.find(m => m.id === messageId);
    if (msg) {
      msg.status = status;
      if (roundTripMs) {
        msg.roundTripMs = roundTripMs;
      }
    }
    
    // Update the rendered message if it's currently visible
    const messageDiv = document.querySelector(`[data-message-id="${messageId}"]`);
    if (messageDiv) {
      updateMessageStatusUI(messageDiv, status, roundTripMs);
    }
  }

  function updateMessageStatusUI(messageDiv, status, roundTripMs) {
    let statusEl = messageDiv.querySelector('.message-status');
    if (!statusEl) {
      statusEl = document.createElement('span');
      statusEl.className = 'message-status';
      const meta = messageDiv.querySelector('.message-meta');
      if (meta) {
        meta.appendChild(document.createTextNode(' '));
        meta.appendChild(statusEl);
      }
    }
    
    // Update status indicator
    if (status === 'sent') {
      statusEl.textContent = '✓'; // Single checkmark
      statusEl.title = 'Sent';
    } else if (status === 'delivered') {
      statusEl.textContent = '✓✓'; // Double checkmark
      if (roundTripMs) {
        statusEl.title = `Delivered (${roundTripMs}ms)`;
      } else {
        statusEl.title = 'Delivered';
      }
    }
  }

  function renderState(state) {
    allContacts = state.contacts || [];
    allChannels = state.channels || [];
    favoriteContacts = new Set(state.favorites || []);
    renderContacts(allContacts);
    renderChannels(allChannels);
    allMessages = state.messages || [];

    // Only render messages if there's a current selection
    if (selectedTarget.type && selectedTarget.id) {
      renderMessagesForSelection();
    } else {
      // No selection - clear messages
      messagesEl.innerHTML = "";
    }
  }

  function renderContacts(contacts) {
    contactsListEl.innerHTML = "";
    if (!contacts) {
      const li = document.createElement("li");
      li.className = "sidebar-empty";
      li.textContent = "Loading...";
      contactsListEl.appendChild(li);
      return;
    }
    if (contacts.length === 0) {
      const li = document.createElement("li");
      li.className = "sidebar-empty";
      li.textContent = "No contacts found";
      contactsListEl.appendChild(li);
      return;
    }
    
    // Sort contacts by lastSeen (most recent first)
    const sortedContacts = [...contacts].sort((a, b) => {
      const timeA = a.lastSeen ? new Date(a.lastSeen).getTime() : 0;
      const timeB = b.lastSeen ? new Date(b.lastSeen).getTime() : 0;
      return timeB - timeA;
    });
    
    // Split into favorites, recent (within 7 days), and older
    const oneWeekAgo = Date.now() - (7 * 24 * 60 * 60 * 1000);
    const favoriteList = [];
    const recentContacts = [];
    const olderContacts = [];
    
    sortedContacts.forEach(c => {
      if (favoriteContacts.has(c.id)) {
        favoriteList.push(c);
      } else {
        const lastSeenTime = c.lastSeen ? new Date(c.lastSeen).getTime() : 0;
        if (lastSeenTime >= oneWeekAgo) {
          recentContacts.push(c);
        } else {
          olderContacts.push(c);
        }
      }
    });
    
    // Render favorite contacts first
    favoriteList.forEach(c => {
      const li = createContactListItem(c, true);
      // Restore selection if this was the selected contact
      if (selectedTarget.type === "contact" && selectedTarget.id === c.id) {
        li.classList.add("selected");
      }
      // Restore unread indicator
      if (unreadCounts[c.id] > 0) {
        li.classList.add("has-unread");
      }
      contactsListEl.appendChild(li);
    });
    
    // Render recent contacts
    recentContacts.forEach(c => {
      const li = createContactListItem(c);
      // Restore selection if this was the selected contact
      if (selectedTarget.type === "contact" && selectedTarget.id === c.id) {
        li.classList.add("selected");
      }
      // Restore unread indicator
      if (unreadCounts[c.id] > 0) {
        li.classList.add("has-unread");
      }
      contactsListEl.appendChild(li);
    });
    
    // Render older contacts in collapsible section
    if (olderContacts.length > 0) {
      const toggleLi = document.createElement("li");
      toggleLi.className = "sidebar-toggle";
      toggleLi.textContent = `▶ Older contacts (${olderContacts.length})`;
      toggleLi.style.cursor = "pointer";
      toggleLi.style.fontWeight = "600";
      toggleLi.style.color = "#9ca3af";
      
      const olderList = document.createElement("div");
      olderList.className = "older-contacts hidden";
      
      olderContacts.forEach(c => {
        const li = createContactListItem(c);
        // Restore selection if this was the selected contact
        if (selectedTarget.type === "contact" && selectedTarget.id === c.id) {
          li.classList.add("selected");
        }
        // Restore unread indicator
        if (unreadCounts[c.id] > 0) {
          li.classList.add("has-unread");
        }
        olderList.appendChild(li);
      });
      
      toggleLi.addEventListener("click", () => {
        olderList.classList.toggle("hidden");
        toggleLi.textContent = olderList.classList.contains("hidden") 
          ? `▶ Older contacts (${olderContacts.length})`
          : `▼ Older contacts (${olderContacts.length})`;
      });
      
      contactsListEl.appendChild(toggleLi);
      contactsListEl.appendChild(olderList);
    }
  }
  
  function createContactListItem(c, isFavorite = false) {
    const li = document.createElement("li");
    li.style.display = "flex";
    li.style.justifyContent = "space-between";
    li.style.alignItems = "center";
    li.dataset.contactId = c.id;
    
    const nameSpan = document.createElement("span");
    nameSpan.textContent = c.name;
    nameSpan.style.flex = "1";
    nameSpan.style.cursor = "pointer";
    nameSpan.addEventListener("click", () => {
      targetTypeEl.value = "contact";
      targetIdEl.value = c.id;

      selectedTarget = { type: "contact", id: c.id };
      
      // Mark as read
      unreadCounts[c.id] = 0;
      updateUnreadIndicators();

      // Update visual selection
      document.querySelectorAll("#contacts-list li").forEach(el => el.classList.remove("selected"));
      document.querySelectorAll("#channels-list li").forEach(el => el.classList.remove("selected"));
      li.classList.add("selected");

      renderMessagesForSelection();        
    });
    
    // Add star button for favorites
    const starBtn = document.createElement("button");
    starBtn.className = "star-btn";
    starBtn.textContent = isFavorite ? "★" : "☆";
    starBtn.title = isFavorite ? "Remove from favorites" : "Add to favorites";
    starBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      toggleFavorite(c.id);
    });
    
    li.appendChild(nameSpan);
    li.appendChild(starBtn);
    return li;
  }

  function renderChannels(channels) {
    channelsListEl.innerHTML = "";
    if (!channels) {
      const li = document.createElement("li");
      li.className = "sidebar-empty";
      li.textContent = "Loading...";
      channelsListEl.appendChild(li);
      return;
    }
    if (channels.length === 0) {
      const li = document.createElement("li");
      li.className = "sidebar-empty";
      li.textContent = "No channels found";
      channelsListEl.appendChild(li);
      return;
    }
    channels.forEach((ch) => {
      const li = document.createElement("li");
      li.style.display = "flex";
      li.style.justifyContent = "space-between";
      li.style.alignItems = "center";
      
      // Don't add # prefix if the name already starts with #
      const displayName = ch.name.startsWith('#') ? ch.name : '#' + ch.name;
      
      const nameSpan = document.createElement("span");
      nameSpan.textContent = displayName + " (" + ch.id + ")";
      nameSpan.style.flex = "1";
      nameSpan.style.cursor = "pointer";
      nameSpan.addEventListener("click", () => {
        targetTypeEl.value = "channel";
        targetIdEl.value = ch.id;

        selectedTarget = { type: "channel", id: ch.id };
        
        // Mark as read
        unreadCounts[ch.id] = 0;
        updateUnreadIndicators();

        // Update visual selection
        document.querySelectorAll("#contacts-list li").forEach(el => el.classList.remove("selected"));
        document.querySelectorAll("#channels-list li").forEach(el => el.classList.remove("selected"));
        li.classList.add("selected");

        console.log(`Selected channel: ${ch.id}, targetIdEl set to: ${targetIdEl.value}`);
        renderMessagesForSelection();
      });
      
      // Add delete button (except for debug channel and channel 0)
      if (ch.id !== "debug" && ch.id !== "ch0") {
        const deleteBtn = document.createElement("button");
        deleteBtn.textContent = "×";
        deleteBtn.className = "delete-btn";
        deleteBtn.title = "Clear this channel slot";
        deleteBtn.addEventListener("click", async (e) => {
          e.stopPropagation();
          if (!confirm(`Clear ${displayName}? This will also delete all messages for this channel.`)) return;
          
          const channelIdx = parseInt(ch.id.replace('ch', ''), 10);
          try {
            const res = await fetch("/api/clear-channel", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ channelIdx })
            });
            const data = await res.json();
            if (!res.ok || !data.success) {
              alert(data.error || "Failed to clear channel");
              return;
            }
            
            // Clear messages for this channel from local cache
            allMessages = allMessages.filter(m => {
              // Remove messages where this channel is sender or recipient
              if (m.isChannel && (m.from === ch.id || m.to === ch.id)) {
                return false;
              }
              return true;
            });
            
            // Clear unread count
            delete unreadCounts[ch.id];
            
            // If this channel was selected, clear the message view
            if (selectedTarget.type === "channel" && selectedTarget.id === ch.id) {
              selectedTarget = { type: null, id: null };
              messagesEl.innerHTML = "";
            }
            
            // Re-render messages if needed
            if (selectedTarget.type && selectedTarget.id) {
              renderMessagesForSelection();
            }
          } catch (e) {
            console.error("Error clearing channel:", e);
            alert("Failed to clear channel");
          }
        });
        li.appendChild(deleteBtn);
      }
      
      li.dataset.channelId = ch.id;
      li.appendChild(nameSpan);
      channelsListEl.appendChild(li);
    });
  }

  function appendMessage(m) {
    const div = document.createElement("div");
    // Add message ID as data attribute for status updates
    div.dataset.messageId = m.id;
    
    // Add alignment class based on sender
    if (m.from === "self") {
      div.className = "message message-sent";
    } else {
      div.className = "message message-received";
    }
    
    // Check for mention in channel messages (only for received messages)
    if (m.isChannel && m.from !== "self" && userName) {
      const mentionPattern = new RegExp(`@\\[${userName}\\]`, 'i');
      if (mentionPattern.test(m.content)) {
        div.classList.add("message-mentioned");
      }
    }

    const meta = document.createElement("div");
    meta.className = "message-meta";
    
    // Format timestamp - use sender time for received messages, timestamp for sent messages
    let displayTime = m.timestamp;
    // if (m.from !== "self" && m.senderTime) {
    //   displayTime = m.senderTime;
    // }
    // Parse as UTC by adding 'Z' suffix if not present, then format in UTC
    if (displayTime) {
      const dateStr = typeof displayTime === 'string' && !displayTime.endsWith('Z') ? displayTime + 'Z' : displayTime;
      const date = new Date(dateStr);
      const hours = date.getUTCHours().toString().padStart(2, '0');
      const minutes = date.getUTCMinutes().toString().padStart(2, '0');
      var ts = `${hours}:${minutes}`;
    } else {
      var ts = "";
    }
    
    // Get sender name
    let senderName = m.from;
    if (m.from === "self") {
      senderName = "You";
    } else if (m.from === "system") {
      senderName = "System";
    } else {
      // Look up contact or channel name
      senderName = lookupName(m.from, m.isChannel);
    }
    
    // Create a span for the sender name with green color
    const nameSpan = document.createElement("span");
    nameSpan.className = "sender-name";
    nameSpan.textContent = senderName;
    
    meta.appendChild(nameSpan);
    meta.appendChild(document.createTextNode(` · ${ts}`));
    
    // Add hop count and latency for received messages
    if (m.from !== "self" && m.from !== "system" && m.pathLen !== undefined && m.senderTime) {
      const hopText = m.pathLen === 255 ? "Direct" : `${m.pathLen} hop${m.pathLen === 1 ? '' : 's'}`;
      
      // Calculate latency (receive time - sender time)
      const receiveTime = new Date(m.timestamp).getTime();
      const senderTime = new Date(m.senderTime).getTime();
      const latencyMs = receiveTime - senderTime;
      const latencyText = latencyMs >= 0 ? `${latencyMs}ms` : "N/A";
      
      const routeInfo = document.createElement('span');
      routeInfo.style.color = '#9ca3af';
      routeInfo.style.fontSize = '0.65rem';
      routeInfo.textContent = ` (${hopText}, ${latencyText})`;
      meta.appendChild(routeInfo);
    }
    
    // Add status indicator for sent messages (contact messages only, not channels)
    if (m.from === "self" && !m.isChannel && m.status) {
      const statusSpan = document.createElement('span');
      statusSpan.className = 'message-status';
      
      if (m.status === 'sent') {
        statusSpan.textContent = '✓'; // Single checkmark
        statusSpan.title = 'Sent';
      } else if (m.status === 'delivered') {
        statusSpan.textContent = '✓✓'; // Double checkmark
        if (m.roundTripMs) {
          statusSpan.title = `Delivered (${m.roundTripMs}ms)`;
        } else {
          statusSpan.title = 'Delivered';
        }
      }
      
      meta.appendChild(document.createTextNode(' '));
      meta.appendChild(statusSpan);
    }

    const body = document.createElement("div");
    
    // Extract sender name from channel message for double-click handler
    let channelSenderName = null;
    
    // For channel messages, highlight the sender name in the content (e.g., "MRZ: message")
    if (m.isChannel && m.from !== "self" && m.content.includes(":")) {
      const colonIndex = m.content.indexOf(":");
      const contentSender = m.content.substring(0, colonIndex);
      const contentMessage = m.content.substring(colonIndex + 1);
      channelSenderName = contentSender;
      
      const senderSpan = document.createElement("span");
      senderSpan.className = "sender-name";
      senderSpan.textContent = contentSender;
      
      body.appendChild(senderSpan);
      body.appendChild(document.createTextNode(":" + contentMessage));
    } else {
      body.textContent = m.content;
    }

    // Add double-click handler for channel messages to insert mention
    if (m.isChannel && channelSenderName && selectedTarget.type === "channel") {
      div.style.cursor = "pointer";
      div.addEventListener("dblclick", () => {
        const mention = `@[${channelSenderName}] `;
        messageInputEl.value = mention;
        messageInputEl.focus();
        updateCharCounter();
      });
    }

    div.appendChild(meta);
    div.appendChild(body);

    messagesEl.appendChild(div);
    messagesEl.scrollTop = messagesEl.scrollHeight;
  }
  
  function lookupName(id, isChannel) {
    if (isChannel) {
      // Look up channel name
      const channelsList = document.querySelectorAll("#channels-list li");
      for (const li of channelsList) {
        if (li.dataset.channelId === id) {
          // Extract just the name part (before the parentheses)
          const text = li.textContent.trim();
          const match = text.match(/^(.+?)\s*\(/);
          return match ? match[1] : text;
        }
      }
      return id; // Fallback to ID
    } else {
      // Look up contact name
      const contactsList = document.querySelectorAll("#contacts-list li");
      for (const li of contactsList) {
        if (li.dataset.contactId === id) {
          return li.textContent.trim();
        }
      }
      return id.substring(0, 12) + "..."; // Fallback to shortened ID
    }
  }

  function messageMatchesSelection(m) {
    if (!selectedTarget.type || !selectedTarget.id) {
      // No selection → don't show anything
      return false;
    }

    if (selectedTarget.type === "contact") {
      const contactId = selectedTarget.id;
      // Non-channel msgs where either side is this contact
      return (
        !m.isChannel &&
        (m.from === contactId || m.to === contactId)
      );
    }

    if (selectedTarget.type === "channel") {
      const chId = selectedTarget.id; // e.g. "ch0"
      // Channel messages for this channel
      // Outbound: from="self", to="chN"
      // Inbound:  from="chN", to="channel"
      return (
        m.isChannel &&
        (m.to === chId || m.from === chId)
      );
    }

    return true;
  }

  function renderMessagesForSelection() {
    messagesEl.innerHTML = "";
    (allMessages || []).forEach((m) => {
      if (messageMatchesSelection(m)) {
        appendMessage(m);
      }
    });
  }

  function updateUnreadIndicators() {
    // Update contact list
    document.querySelectorAll("#contacts-list li").forEach(li => {
      const contactId = li.dataset.contactId;
      if (contactId && unreadCounts[contactId] > 0) {
        li.classList.add("has-unread");
      } else {
        li.classList.remove("has-unread");
      }
    });
    
    // Update channel list
    document.querySelectorAll("#channels-list li").forEach(li => {
      const channelId = li.dataset.channelId;
      if (channelId && unreadCounts[channelId] > 0) {
        li.classList.add("has-unread");
      } else {
        li.classList.remove("has-unread");
      }
    });
  }

  // Update character counter
  function updateCharCounter() {
    const length = messageInputEl.value.length;
    charCounterEl.textContent = `${length} / 150`;
    if (length > 150) {
      charCounterEl.style.color = "#ef4444"; // red
    } else if (length > 130) {
      charCounterEl.style.color = "#f59e0b"; // orange
    } else {
      charCounterEl.style.color = "#9ca3af"; // gray
    }
  }

  messageInputEl.addEventListener("input", updateCharCounter);

  sendBtnEl.addEventListener("click", () => {
    const targetId = targetIdEl.value.trim();
    const content = messageInputEl.value.trim();
    console.log(`[Send Button] targetId=${targetId}, targetType=${targetTypeEl.value}, content=${content}`);
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      alert("Not connected");
      return;
    }
    if (!targetId || !content) {
      console.log(`[Send Button] Validation failed: targetId or content is empty`);
      return;
    }
    if (content.length > 150) {
      alert("Message is too long. Maximum 150 characters allowed.");
      return;
    }
    const payload = {
      type: "send_message",
      targetId,
      targetType: targetTypeEl.value,
      content,
    };
    console.log(`[Send Button] Sending payload:`, payload);
    ws.send(JSON.stringify(payload));
    messageInputEl.value = "";
    updateCharCounter();
  });

  messageInputEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      sendBtnEl.click();
    }
  });

  connectBtnEl.addEventListener("click", () => {
    if (nodeConnected) {
      disconnectFromNode();
    } else {
      openConnectModal();
    }
  });

  connectCancelEl.addEventListener("click", () => {
    closeConnectModal();
  });

  connectConfirmEl.addEventListener("click", () => {
    const host = (nodeHostEl.value || "127.0.0.1").trim();
    const port = parseInt((nodePortEl.value || "5000").trim(), 10) || 5000;
    console.log("[connectConfirmEl] 'Connect' clicked. Calling connectToNode.");
    connectToNode(host, port);
  });

  // Allow Enter key to trigger connect in modal inputs
  nodeHostEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      connectConfirmEl.click();
    }
  });

  nodePortEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      connectConfirmEl.click();
    }
  });

  addChannelBtnEl.addEventListener("click", () => {
    if (!nodeConnected) {
      alert("Please connect to a node first");
      return;
    }
    openAddChannelModal();
  });

  addChannelCancelEl.addEventListener("click", () => {
    closeAddChannelModal();
  });

  addChannelConfirmEl.addEventListener("click", async () => {
    const channelIdx = parseInt(channelIndexEl.value, 10);
    if (isNaN(channelIdx) || channelIdx < 0 || channelIdx > 3) {
      alert("Please enter a valid channel index (0-3)");
      return;
    }

    const name = channelNameEl.value.trim();
    if (!name) {
      alert("Please enter a channel name");
      return;
    }

    let secret = channelSecretEl.value.trim();
    
    // For public channels (hashtag channels), require explicit secret
    if (name.startsWith('#') && !secret) {
      alert("Public channels (starting with #) require a shared secret. Use the default public channel secret: 8b3387e9c5cdea6ac9e5edbaa115cd72 or share a secret with your community.");
      return;
    }
    
    // Generate random secret if not provided (for private channels)
    if (!secret) {
      const randomBytes = new Uint8Array(16);
      crypto.getRandomValues(randomBytes);
      secret = Array.from(randomBytes).map(b => b.toString(16).padStart(2, '0')).join('');
      console.log("Generated random secret:", secret);
    }

    // Validate secret (must be 32 hex characters)
    if (!/^[0-9a-fA-F]{32}$/.test(secret)) {
      alert("Secret must be exactly 32 hexadecimal characters (0-9, a-f)");
      return;
    }

    try {
      const res = await fetch("/api/add-channel", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          channelIdx: channelIdx,
          name: name,
          secret: secret
        })
      });

      const data = await res.json();
      if (!res.ok || !data.success) {
        alert(data.error || "Failed to add channel");
        return;
      }

      console.log("Channel added successfully");
      closeAddChannelModal();
    } catch (e) {
      console.error("Error adding channel:", e);
      alert("Failed to add channel");
    }
  });

  function openAddChannelModal() {
    addChannelModalEl.classList.remove("hidden");
    channelIndexEl.value = "0";
    channelNameEl.value = "";
    channelSecretEl.value = "";
    channelIndexEl.focus();
  }

  // Update placeholder based on channel name
  channelNameEl.addEventListener("input", () => {
    const name = channelNameEl.value.trim();
    if (name.startsWith('#')) {
      channelSecretEl.placeholder = "Required: Use shared public channel secret (e.g. 8b3387e9c5cdea6ac9e5edbaa115cd72)";
    } else {
      channelSecretEl.placeholder = "Leave empty to generate random secret";
    }
  });

  function closeAddChannelModal() {
    addChannelModalEl.classList.add("hidden");
  }

  // Advert buttons
  sendZeroHopAdvertBtn.addEventListener("click", () => {
    if (!nodeConnected) {
      alert("Please connect to a node first");
      return;
    }
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      alert("WebSocket not connected");
      return;
    }
    ws.send(JSON.stringify({ type: "send_zero_hop_advert" }));
    console.log("Sent zero-hop advert request");
  });

  sendFloodAdvertBtn.addEventListener("click", () => {
    if (!nodeConnected) {
      alert("Please connect to a node first");
      return;
    }
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      alert("WebSocket not connected");
      return;
    }
    ws.send(JSON.stringify({ type: "send_flood_advert" }));
    console.log("Sent flood advert request");
  });

  // Initialize
  setStatus(false);
  updateNodeStatus({ connected: false, address: "" });
  connectWs();
})();
