// C-Suite PWA — Voice Controls (STT + TTS)
// Speech-to-text for voice input, text-to-speech for reading agent messages.
// Gracefully degrades when Web Speech API is not available.

(function () {
  'use strict';

  // -------------------------------------------------------------------------
  // Feature detection
  // -------------------------------------------------------------------------

  const SpeechRecognition = window.SpeechRecognition || window.webkitSpeechRecognition;
  const synth = window.speechSynthesis;

  const hasSTT = !!SpeechRecognition;
  const hasTTS = !!synth;

  if (!hasSTT && !hasTTS) {
    // No speech APIs available — nothing to do.
    return;
  }

  // -------------------------------------------------------------------------
  // DOM refs
  // -------------------------------------------------------------------------

  const $inputBar = document.querySelector('.input-bar');
  const $msgInput = document.getElementById('msg-input');
  const $sendBtn = document.getElementById('send-btn');
  const $messagesArea = document.getElementById('messages-area');

  if (!$inputBar || !$msgInput) return;

  // -------------------------------------------------------------------------
  // STT — Microphone button
  // -------------------------------------------------------------------------

  let recognition = null;
  let isRecording = false;
  let $micBtn = null;

  if (hasSTT) {
    // Create mic button and insert before send button.
    $micBtn = document.createElement('button');
    $micBtn.id = 'mic-btn';
    $micBtn.className = 'mic-btn';
    $micBtn.setAttribute('aria-label', 'Voice input');
    $micBtn.innerHTML = '<svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor">' +
      '<path d="M12 14a3 3 0 0 0 3-3V5a3 3 0 0 0-6 0v6a3 3 0 0 0 3 3z"/>' +
      '<path d="M19 11a1 1 0 0 0-2 0 5 5 0 0 1-10 0 1 1 0 0 0-2 0 7 7 0 0 0 6 6.93V21h-3a1 1 0 0 0 0 2h8a1 1 0 0 0 0-2h-3v-3.07A7 7 0 0 0 19 11z"/>' +
      '</svg>';
    $inputBar.insertBefore($micBtn, $sendBtn);

    // Recording indicator overlay.
    const $recordingIndicator = document.createElement('div');
    $recordingIndicator.id = 'recording-indicator';
    $recordingIndicator.className = 'recording-indicator hidden';
    $recordingIndicator.innerHTML =
      '<div class="recording-pulse"></div>' +
      '<span>Listening...</span>' +
      '<button id="recording-stop" class="recording-stop" aria-label="Stop recording">Stop</button>';
    document.body.appendChild($recordingIndicator);

    const $recordingStop = document.getElementById('recording-stop');

    // Initialize recognition.
    recognition = new SpeechRecognition();
    recognition.continuous = false;
    recognition.interimResults = true;
    recognition.lang = navigator.language || 'en-US';
    recognition.maxAlternatives = 1;

    recognition.onstart = function () {
      isRecording = true;
      $micBtn.classList.add('recording');
      $recordingIndicator.classList.remove('hidden');
    };

    recognition.onresult = function (event) {
      let transcript = '';
      for (let i = 0; i < event.results.length; i++) {
        transcript += event.results[i][0].transcript;
      }
      // Place transcript into the input field.
      $msgInput.value = transcript;
      // Trigger input event so auto-resize works.
      $msgInput.dispatchEvent(new Event('input'));
    };

    recognition.onerror = function (event) {
      // 'no-speech' and 'aborted' are not real errors — just stop gracefully.
      if (event.error !== 'no-speech' && event.error !== 'aborted') {
        console.warn('Speech recognition error:', event.error);
      }
      stopRecording();
    };

    recognition.onend = function () {
      stopRecording();
    };

    function startRecording() {
      if (isRecording) {
        stopRecording();
        return;
      }
      try {
        recognition.start();
      } catch (e) {
        console.warn('Could not start speech recognition:', e);
      }
    }

    function stopRecording() {
      isRecording = false;
      $micBtn.classList.remove('recording');
      $recordingIndicator.classList.add('hidden');
      try {
        recognition.stop();
      } catch (_) { /* already stopped */ }
    }

    $micBtn.addEventListener('click', function (e) {
      e.preventDefault();
      startRecording();
    });

    $recordingStop.addEventListener('click', function (e) {
      e.preventDefault();
      stopRecording();
    });
  }

  // -------------------------------------------------------------------------
  // TTS — Speaker buttons on agent messages
  // -------------------------------------------------------------------------

  let currentUtterance = null;
  let speakingMessageId = null;

  if (hasTTS) {
    // Use a MutationObserver to attach speaker buttons as new messages appear.
    const observer = new MutationObserver(function (mutations) {
      mutations.forEach(function (mutation) {
        mutation.addedNodes.forEach(function (node) {
          if (node.nodeType === Node.ELEMENT_NODE) {
            attachSpeakerButtons(node);
          }
        });
      });
    });

    observer.observe($messagesArea, { childList: true });

    // Also attach to any existing agent messages.
    attachSpeakerButtons($messagesArea);
  }

  function attachSpeakerButtons(root) {
    if (!hasTTS) return;

    const agentMessages = root.classList && root.classList.contains('message') && root.classList.contains('agent')
      ? [root]
      : root.querySelectorAll ? root.querySelectorAll('.message.agent') : [];

    agentMessages.forEach(function (msg) {
      // Skip if already has a speaker button.
      if (msg.querySelector('.speak-btn')) return;

      const msgId = msg.getAttribute('data-id');
      const bodyEl = msg.querySelector('div:not(.subject):not(.meta)');
      if (!bodyEl) return;

      const btn = document.createElement('button');
      btn.className = 'speak-btn';
      btn.setAttribute('aria-label', 'Read aloud');
      btn.innerHTML = '<svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor">' +
        '<path d="M3 9v6h4l5 5V4L7 9H3z"/>' +
        '<path d="M16.5 12c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02z"/>' +
        '<path d="M14 3.23v2.06c2.89.86 5 3.54 5 6.71s-2.11 5.85-5 6.71v2.06c4.01-.91 7-4.49 7-8.77s-2.99-7.86-7-8.77z"/>' +
        '</svg>';

      btn.addEventListener('click', function (e) {
        e.preventDefault();
        e.stopPropagation();

        // If already speaking this message, stop.
        if (speakingMessageId === msgId) {
          stopSpeaking();
          return;
        }

        // Stop any current speech.
        stopSpeaking();

        const text = bodyEl.textContent;
        if (!text.trim()) return;

        const utterance = new SpeechSynthesisUtterance(text);
        utterance.rate = 1.0;
        utterance.pitch = 1.0;

        speakingMessageId = msgId;
        currentUtterance = utterance;
        btn.classList.add('speaking');
        msg.classList.add('speaking');

        utterance.onend = function () {
          clearSpeakingState(btn, msg);
        };

        utterance.onerror = function () {
          clearSpeakingState(btn, msg);
        };

        synth.speak(utterance);
      });

      // Insert speaker button before the meta line.
      const meta = msg.querySelector('.meta');
      if (meta) {
        meta.parentNode.insertBefore(btn, meta);
      } else {
        msg.appendChild(btn);
      }
    });
  }

  function stopSpeaking() {
    if (synth.speaking) {
      synth.cancel();
    }
    // Clear all speaking states.
    var btns = $messagesArea.querySelectorAll('.speak-btn.speaking');
    btns.forEach(function (b) { b.classList.remove('speaking'); });
    var msgs = $messagesArea.querySelectorAll('.message.speaking');
    msgs.forEach(function (m) { m.classList.remove('speaking'); });
    speakingMessageId = null;
    currentUtterance = null;
  }

  function clearSpeakingState(btn, msg) {
    btn.classList.remove('speaking');
    msg.classList.remove('speaking');
    speakingMessageId = null;
    currentUtterance = null;
  }

})();
