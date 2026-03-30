// C-Suite PWA — Application logic
// Vanilla JS, no frameworks. Connects to the bridge server REST API.

(function () {
  'use strict';

  // -------------------------------------------------------------------------
  // State
  // -------------------------------------------------------------------------

  let token = localStorage.getItem('csuite_token') || '';
  let agents = []; // [{name, status, context_percent, current_activity, unread_count}]
  let activeAgent = localStorage.getItem('csuite_active_agent') || '';
  let messages = []; // current agent's messages [{id, from_agent, to_agent, subject, body, created_at}]
  let loading = false;
  let oldestMessageId = null; // cursor for scroll-back pagination
  let ws = null; // WebSocket connection
  let wsRetryDelay = 1000;
  const wsMaxRetry = 30000;
  const POLL_INTERVAL = 5000; // REST fallback poll interval (ms)
  let pollTimer = null;

  // Quick-action buttons — stored in localStorage.
  const DEFAULT_QUICK_ACTIONS = [
    { label: 'status', payload: 'status' },
    { label: 'check', payload: 'check' },
    { label: 'yes', payload: 'yes' },
  ];

  function getQuickActions() {
    try {
      const stored = localStorage.getItem('csuite_quick_actions');
      if (stored) return JSON.parse(stored);
    } catch (_) { /* ignore */ }
    return DEFAULT_QUICK_ACTIONS;
  }

  // -------------------------------------------------------------------------
  // DOM refs
  // -------------------------------------------------------------------------

  const $login = document.getElementById('login-screen');
  const $loginInput = document.getElementById('login-token');
  const $loginBtn = document.getElementById('login-btn');
  const $loginError = document.getElementById('login-error');
  const $app = document.getElementById('app');
  const $connStatus = document.getElementById('connection-status');
  const $agentTabs = document.getElementById('agent-tabs');
  const $messagesArea = document.getElementById('messages-area');
  const $quickActions = document.getElementById('quick-actions');
  const $msgInput = document.getElementById('msg-input');
  const $sendBtn = document.getElementById('send-btn');

  // -------------------------------------------------------------------------
  // API helpers
  // -------------------------------------------------------------------------

  function apiHeaders() {
    return { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' };
  }

  async function apiFetch(path, opts) {
    const resp = await fetch(path, Object.assign({ headers: apiHeaders() }, opts));
    if (resp.status === 401) {
      logout();
      throw new Error('unauthorized');
    }
    return resp;
  }

  // -------------------------------------------------------------------------
  // Auth
  // -------------------------------------------------------------------------

  function showLogin() {
    $login.style.display = 'flex';
    $app.classList.remove('active');
    stopPoll();
    closeWs();
  }

  function showApp() {
    $login.style.display = 'none';
    $app.classList.add('active');
  }

  function logout() {
    token = '';
    localStorage.removeItem('csuite_token');
    showLogin();
  }

  async function attemptLogin() {
    const val = $loginInput.value.trim();
    if (!val) { $loginError.textContent = 'Token is required.'; return; }
    $loginError.textContent = '';
    $loginBtn.disabled = true;

    try {
      const resp = await fetch('/api/health', {
        headers: { Authorization: 'Bearer ' + val },
      });
      if (!resp.ok) {
        $loginError.textContent = 'Invalid token.';
        return;
      }
      token = val;
      localStorage.setItem('csuite_token', token);
      $loginInput.value = '';
      bootstrap();
    } catch (e) {
      $loginError.textContent = 'Connection failed.';
    } finally {
      $loginBtn.disabled = false;
    }
  }

  // -------------------------------------------------------------------------
  // Bootstrap — load agents and initial messages
  // -------------------------------------------------------------------------

  async function bootstrap() {
    showApp();
    await loadAgents();
    if (agents.length > 0) {
      if (!activeAgent || !agents.find(a => a.name === activeAgent)) {
        activeAgent = agents[0].name;
        localStorage.setItem('csuite_active_agent', activeAgent);
      }
      renderAgentTabs();
      await loadMessages();
    }
    renderQuickActions();
    connectWs();
    startPoll();
  }

  // -------------------------------------------------------------------------
  // Agents
  // -------------------------------------------------------------------------

  async function loadAgents() {
    try {
      const resp = await apiFetch('/api/agents');
      if (resp.ok) {
        agents = await resp.json();
        if (!Array.isArray(agents)) agents = [];
      }
    } catch (_) { /* offline or error */ }
    renderAgentTabs();
  }

  function renderAgentTabs() {
    $agentTabs.innerHTML = '';
    agents.forEach(a => {
      const btn = document.createElement('button');
      btn.className = 'agent-tab' + (a.name === activeAgent ? ' active' : '');
      btn.setAttribute('data-agent', a.name);

      // Status dot
      const dot = document.createElement('span');
      dot.className = 'status-dot' + (a.status ? ' ' + a.status : '');
      btn.appendChild(dot);

      // Name
      btn.appendChild(document.createTextNode(a.name));

      // Unread badge
      if (a.unread_count > 0) {
        const badge = document.createElement('span');
        badge.className = 'badge';
        badge.textContent = a.unread_count > 99 ? '99+' : a.unread_count;
        btn.appendChild(badge);
      }

      btn.addEventListener('click', () => selectAgent(a.name));
      $agentTabs.appendChild(btn);
    });
  }

  async function selectAgent(name) {
    if (name === activeAgent) return;
    activeAgent = name;
    localStorage.setItem('csuite_active_agent', name);
    renderAgentTabs();
    messages = [];
    oldestMessageId = null;
    await loadMessages();
  }

  // -------------------------------------------------------------------------
  // Messages
  // -------------------------------------------------------------------------

  async function loadMessages(beforeId) {
    if (!activeAgent || loading) return;
    loading = true;
    renderLoading(true);

    try {
      let url = '/api/messages?from=operator&to=' + encodeURIComponent(activeAgent) + '&limit=50';
      if (beforeId) url += '&before_id=' + encodeURIComponent(beforeId);

      const resp = await apiFetch(url);
      if (resp.ok) {
        const batch = await resp.json();
        if (Array.isArray(batch) && batch.length > 0) {
          if (beforeId) {
            // Prepend older messages (they come newest-first from API)
            messages = batch.reverse().concat(messages);
          } else {
            // Initial load — reverse so oldest is first
            messages = batch.reverse();
          }
          oldestMessageId = messages[0] ? messages[0].id : null;
        }
      }
    } catch (_) { /* offline */ }

    loading = false;
    renderLoading(false);
    renderMessages(!beforeId); // scroll to bottom only on initial load
  }

  function renderMessages(scrollToBottom) {
    if (messages.length === 0) {
      $messagesArea.innerHTML = '<div class="empty-state">No messages yet.<br>Send a message to start the conversation.</div>';
      return;
    }

    const scrollPos = $messagesArea.scrollTop;
    const scrollHeight = $messagesArea.scrollHeight;

    $messagesArea.innerHTML = '';
    messages.forEach(m => {
      const div = document.createElement('div');
      const isOperator = m.from_agent === 'operator';
      div.className = 'message ' + (isOperator ? 'operator' : 'agent');
      div.setAttribute('data-id', m.id);

      // Subject line (if present and non-empty)
      if (m.subject) {
        const subj = document.createElement('div');
        subj.className = 'subject';
        subj.textContent = m.subject;
        div.appendChild(subj);
      }

      // Body
      const body = document.createElement('div');
      body.textContent = m.body;
      div.appendChild(body);

      // Timestamp
      const meta = document.createElement('div');
      meta.className = 'meta';
      meta.textContent = formatTime(m.created_at);
      div.appendChild(meta);

      $messagesArea.appendChild(div);
    });

    if (scrollToBottom) {
      $messagesArea.scrollTop = $messagesArea.scrollHeight;
    } else {
      // Preserve scroll position when prepending older messages
      const newScrollHeight = $messagesArea.scrollHeight;
      $messagesArea.scrollTop = scrollPos + (newScrollHeight - scrollHeight);
    }
  }

  function renderLoading(show) {
    const existing = $messagesArea.querySelector('.loading-indicator');
    if (show && !existing) {
      const el = document.createElement('div');
      el.className = 'loading-indicator';
      el.textContent = 'Loading...';
      $messagesArea.prepend(el);
    } else if (!show && existing) {
      existing.remove();
    }
  }

  function formatTime(iso) {
    try {
      const d = new Date(iso);
      return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    } catch (_) {
      return '';
    }
  }

  // -------------------------------------------------------------------------
  // Send message
  // -------------------------------------------------------------------------

  async function sendMessage(body) {
    if (!body.trim() || !activeAgent) return;

    const payload = {
      from_agent: 'operator',
      to_agent: activeAgent,
      subject: 'chat',
      body: body.trim(),
      priority: 'normal',
      type: 'request',
    };

    // Try WebSocket first, fall back to REST.
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'send_message', data: payload }));
    } else {
      try {
        const resp = await apiFetch('/api/messages', {
          method: 'POST',
          body: JSON.stringify(payload),
        });
        if (resp.ok) {
          const msg = await resp.json();
          appendMessage(msg);
        }
      } catch (e) {
        // Best-effort — message may not have been sent.
        console.error('send failed:', e);
      }
    }

    $msgInput.value = '';
    autoResizeInput();
  }

  function appendMessage(msg) {
    // Avoid duplicates
    if (messages.find(m => m.id === msg.id)) return;
    // Only append if it belongs to the active conversation
    if (
      (msg.from_agent === 'operator' && msg.to_agent === activeAgent) ||
      (msg.from_agent === activeAgent && msg.to_agent === 'operator')
    ) {
      messages.push(msg);
      renderMessages(true);
    }
  }

  // -------------------------------------------------------------------------
  // Quick Actions
  // -------------------------------------------------------------------------

  function saveQuickActions(actions) {
    localStorage.setItem('csuite_quick_actions', JSON.stringify(actions));
  }

  function renderQuickActions() {
    const actions = getQuickActions();
    $quickActions.innerHTML = '';
    actions.forEach(a => {
      const btn = document.createElement('button');
      btn.className = 'quick-btn';
      btn.textContent = a.label;
      btn.addEventListener('click', () => sendMessage(a.payload));
      $quickActions.appendChild(btn);
    });

    // Gear icon for configuration
    const gear = document.createElement('button');
    gear.className = 'quick-btn qa-gear-btn';
    gear.innerHTML = '&#9881;';
    gear.setAttribute('aria-label', 'Configure quick actions');
    gear.addEventListener('click', openQaConfig);
    $quickActions.appendChild(gear);
  }

  // -------------------------------------------------------------------------
  // Quick Actions Configuration
  // -------------------------------------------------------------------------

  const $qaOverlay = document.getElementById('qa-config-overlay');
  const $qaConfigList = document.getElementById('qa-config-list');
  const $qaCloseBtn = document.getElementById('qa-config-close');
  const $qaAddBtn = document.getElementById('qa-config-add');
  const $qaResetBtn = document.getElementById('qa-config-reset');

  function openQaConfig() {
    renderQaConfigList();
    $qaOverlay.classList.remove('hidden');
  }

  function closeQaConfig() {
    $qaOverlay.classList.add('hidden');
    renderQuickActions();
  }

  function renderQaConfigList() {
    const actions = getQuickActions();
    $qaConfigList.innerHTML = '';

    if (actions.length === 0) {
      const empty = document.createElement('div');
      empty.className = 'qa-config-empty';
      empty.textContent = 'No quick actions. Tap "+ Add Action" to create one.';
      $qaConfigList.appendChild(empty);
      return;
    }

    actions.forEach(function (action, index) {
      var item = document.createElement('div');
      item.className = 'qa-config-item';
      item.setAttribute('data-index', index);

      // Reorder buttons
      var reorderGroup = document.createElement('div');
      reorderGroup.className = 'qa-config-reorder';

      var upBtn = document.createElement('button');
      upBtn.className = 'qa-config-arrow';
      upBtn.innerHTML = '&#9650;';
      upBtn.setAttribute('aria-label', 'Move up');
      upBtn.disabled = (index === 0);
      upBtn.addEventListener('click', function () { moveQaAction(index, -1); });

      var downBtn = document.createElement('button');
      downBtn.className = 'qa-config-arrow';
      downBtn.innerHTML = '&#9660;';
      downBtn.setAttribute('aria-label', 'Move down');
      downBtn.disabled = (index === actions.length - 1);
      downBtn.addEventListener('click', function () { moveQaAction(index, 1); });

      reorderGroup.appendChild(upBtn);
      reorderGroup.appendChild(downBtn);
      item.appendChild(reorderGroup);

      // Fields
      var fields = document.createElement('div');
      fields.className = 'qa-config-fields';

      var labelInput = document.createElement('input');
      labelInput.type = 'text';
      labelInput.className = 'qa-config-input';
      labelInput.placeholder = 'Label';
      labelInput.value = action.label;
      labelInput.setAttribute('data-field', 'label');
      labelInput.setAttribute('data-index', index);
      labelInput.addEventListener('change', function () {
        updateQaAction(index, 'label', this.value);
      });

      var payloadInput = document.createElement('input');
      payloadInput.type = 'text';
      payloadInput.className = 'qa-config-input';
      payloadInput.placeholder = 'Payload';
      payloadInput.value = action.payload;
      payloadInput.setAttribute('data-field', 'payload');
      payloadInput.setAttribute('data-index', index);
      payloadInput.addEventListener('change', function () {
        updateQaAction(index, 'payload', this.value);
      });

      fields.appendChild(labelInput);
      fields.appendChild(payloadInput);
      item.appendChild(fields);

      // Delete button
      var delBtn = document.createElement('button');
      delBtn.className = 'qa-config-delete';
      delBtn.innerHTML = '&#128465;';
      delBtn.setAttribute('aria-label', 'Remove action');
      delBtn.addEventListener('click', function () { removeQaAction(index); });
      item.appendChild(delBtn);

      $qaConfigList.appendChild(item);
    });
  }

  function moveQaAction(index, direction) {
    var actions = getQuickActions();
    var target = index + direction;
    if (target < 0 || target >= actions.length) return;

    var temp = actions[index];
    actions[index] = actions[target];
    actions[target] = temp;

    saveQuickActions(actions);
    renderQaConfigList();
  }

  function updateQaAction(index, field, value) {
    var actions = getQuickActions();
    if (index >= 0 && index < actions.length) {
      actions[index][field] = value;
      saveQuickActions(actions);
    }
  }

  function removeQaAction(index) {
    var actions = getQuickActions();
    actions.splice(index, 1);
    saveQuickActions(actions);
    renderQaConfigList();
  }

  function addQaAction() {
    var actions = getQuickActions();
    actions.push({ label: '', payload: '' });
    saveQuickActions(actions);
    renderQaConfigList();
    // Focus the new label input
    var inputs = $qaConfigList.querySelectorAll('.qa-config-input[data-field="label"]');
    if (inputs.length > 0) {
      inputs[inputs.length - 1].focus();
    }
  }

  function resetQaActions() {
    saveQuickActions(DEFAULT_QUICK_ACTIONS.slice());
    renderQaConfigList();
  }

  $qaCloseBtn.addEventListener('click', closeQaConfig);
  $qaAddBtn.addEventListener('click', addQaAction);
  $qaResetBtn.addEventListener('click', resetQaActions);

  // Close on overlay background click
  $qaOverlay.addEventListener('click', function (e) {
    if (e.target === $qaOverlay) closeQaConfig();
  });

  // -------------------------------------------------------------------------
  // WebSocket
  // -------------------------------------------------------------------------

  function connectWs() {
    if (!token) return;
    closeWs();

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = proto + '//' + location.host + '/api/ws?token=' + encodeURIComponent(token);

    try {
      ws = new WebSocket(url);
    } catch (_) {
      scheduleWsRetry();
      return;
    }

    ws.onopen = () => {
      wsRetryDelay = 1000;
      setConnectionStatus('connected');
    };

    ws.onclose = () => {
      setConnectionStatus('disconnected');
      scheduleWsRetry();
    };

    ws.onerror = () => {
      // onclose will fire after onerror
    };

    ws.onmessage = (event) => {
      try {
        const evt = JSON.parse(event.data);
        handleWsEvent(evt);
      } catch (_) { /* ignore malformed */ }
    };
  }

  function closeWs() {
    if (ws) {
      ws.onclose = null;
      ws.onerror = null;
      ws.close();
      ws = null;
    }
  }

  function scheduleWsRetry() {
    setTimeout(() => {
      if (token) connectWs();
    }, wsRetryDelay);
    wsRetryDelay = Math.min(wsRetryDelay * 2, wsMaxRetry);
  }

  function handleWsEvent(evt) {
    switch (evt.type) {
      case 'new_message':
        if (evt.data) appendMessage(evt.data);
        // Refresh agent tabs to update unread counts
        loadAgents();
        break;
      case 'connected':
        // Welcome event — no action needed.
        break;
      case 'pong':
        break;
      default:
        break;
    }
  }

  function setConnectionStatus(state) {
    $connStatus.className = 'connection-status ' + state;
    $connStatus.textContent = state === 'connected' ? 'Connected' : 'Disconnected';
  }

  // -------------------------------------------------------------------------
  // REST Polling (fallback for agent refresh)
  // -------------------------------------------------------------------------

  function startPoll() {
    stopPoll();
    pollTimer = setInterval(() => {
      loadAgents();
    }, POLL_INTERVAL);
  }

  function stopPoll() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  // -------------------------------------------------------------------------
  // Input handling
  // -------------------------------------------------------------------------

  function autoResizeInput() {
    $msgInput.style.height = 'auto';
    $msgInput.style.height = Math.min($msgInput.scrollHeight, 120) + 'px';
  }

  $msgInput.addEventListener('input', autoResizeInput);

  $msgInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendMessage($msgInput.value);
    }
  });

  $sendBtn.addEventListener('click', () => {
    sendMessage($msgInput.value);
  });

  $loginBtn.addEventListener('click', attemptLogin);
  $loginInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') attemptLogin();
  });

  // Scroll-back: load older messages when scrolled to top
  $messagesArea.addEventListener('scroll', () => {
    if ($messagesArea.scrollTop < 50 && !loading && oldestMessageId && messages.length > 0) {
      loadMessages(oldestMessageId);
    }
  });

  // -------------------------------------------------------------------------
  // Service Worker registration
  // -------------------------------------------------------------------------

  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js').catch(err => {
      console.warn('SW registration failed:', err);
    });
  }

  // -------------------------------------------------------------------------
  // Init
  // -------------------------------------------------------------------------

  if (token) {
    bootstrap();
  } else {
    showLogin();
  }
})();
