'use strict';

(() => {
  const $ = (id) => document.getElementById(id);
  const state = {
    authenticated: false, entries: [], jobs: [], entryJSON: '', jobJSON: '',
    selectedDate: '', query: '', target: null, expanded: new Set(), dismissed: new Set(),
    year: 0, month: 0, calendarInitialized: false, refreshBusy: false, lastEntries: 0,
    modalVersion: 0, toastTimer: null, answers: new Map(), pendingRequests: new Map(),
  };
  const dateParts = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Seoul', year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(new Date());
  const today = ['year', 'month', 'day'].map((part) => dateParts.find((p) => p.type === part).value).join('-');
  [state.year, state.month] = today.split('-').map(Number);
  const weekdays = ['일', '월', '화', '수', '목', '금', '토'];
  const statusLabels = { queued: '차례를 기다리고 있어요', running: '이야기를 정리하고 있어요', saving: '기록에 저장하고 있어요', needs_input: '조금만 더 알려 주세요', completed: '기록에 담았어요', failed: '이번에는 정리하지 못했어요', cancelled: '정리를 멈췄어요' };
  const iconPaths = { add: 'M12 5v14M5 12h14', edit: 'm16 3 5 5-13 13H3v-5L16 3ZM13 6l5 5', pin: 'M20 10c0 6-8 12-8 12S4 16 4 10a8 8 0 1 1 16 0ZM15 10a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z' };

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }
  function icon(name) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('aria-hidden', 'true');
    const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    path.setAttribute('d', iconPaths[name]);
    svg.append(path);
    return svg;
  }
  function button(text, className, handler) {
    const node = el('button', className, text);
    node.type = 'button';
    if (handler) node.addEventListener('click', handler);
    return node;
  }
  function formatDate(value, short = false) {
    const [year, month, day] = value.split('-').map(Number);
    if (!year || !month || !day) return value;
    const weekday = weekdays[new Date(year, month - 1, day).getDay()];
    return short ? `${month}월 ${day}일 (${weekday})` : `${year}년 ${month}월 ${day}일 ${weekday}요일`;
  }
  function formatTime(value) {
    const d = new Date(value);
    if (Number.isNaN(d.getTime())) return '';
    return new Intl.DateTimeFormat('ko-KR', { timeZone: 'Asia/Seoul', year: 'numeric', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(d);
  }
  function prettyTitle(entry) { return (entry.title || entry.id || '').replace(/-/g, ' · '); }
  function changeLabel(change) { return (change.description || '').split('\n').map((line) => line.trim()).find(Boolean) || '기록 변경'; }
  function setError(id, message) { $(id).textContent = message || ''; $(id).hidden = !message; }
  function toast(message, error = false) {
    clearTimeout(state.toastTimer);
    $('toast').textContent = message;
    $('toast').className = `toast${error ? ' error' : ''}`;
    $('toast').hidden = false;
    state.toastTimer = setTimeout(() => { $('toast').hidden = true; }, error ? 6500 : 4000);
  }
  async function api(path, options = {}) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 25000);
    try {
      const response = await fetch(path, {
        credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        ...options, headers: { ...(options.body ? { 'Content-Type': 'application/json' } : {}), ...options.headers },
      });
      const text = await response.text();
      let data;
      try { data = text ? JSON.parse(text) : {}; } catch { throw new Error('서버의 응답을 읽지 못했어요. 잠시 후 다시 시도해 주세요.'); }
      if (!response.ok) {
        if (response.status === 401 && path !== '/api/session') showLogin();
        const message = typeof data.error === 'string' ? data.error : data.error?.message || data.message;
        throw new Error(message || (response.status === 401 ? '접속 키를 다시 확인해 주세요.' : '요청을 처리하지 못했어요. 다시 시도해 주세요.'));
      }
      return data;
    } catch (error) {
      if (error.name === 'AbortError') throw new Error('응답이 늦어지고 있어요. 연결을 확인한 뒤 다시 시도해 주세요.');
      if (error instanceof TypeError) throw new Error('서버에 연결할 수 없어요. 잠시 후 다시 시도해 주세요.');
      throw error;
    } finally { clearTimeout(timer); }
  }
  const post = (path, body = {}) => api(path, { method: 'POST', body: JSON.stringify(body) });

  function showLogin() {
    state.authenticated = false;
    state.entries = []; state.jobs = []; state.entryJSON = ''; state.jobJSON = '';
    state.target = null; state.selectedDate = ''; state.query = '';
    $('search').value = ''; $('compose-message').value = '';
    $('feed').replaceChildren(); $('jobs').replaceChildren();
    closeModal();
    $('app-view').hidden = true; $('login-view').hidden = false; $('loading').hidden = true;
    $('access-key').focus();
  }
  async function showApp() {
    state.authenticated = true;
    $('access-key').value = '';
    $('login-view').hidden = true; $('app-view').hidden = false; $('loading').hidden = true;
    setError('login-error', ''); renderTarget();
    await refresh(true);
  }
  $('login-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    const submit = $('login-button'); submit.disabled = true; submit.textContent = '기록장을 여는 중…';
    setError('login-error', '');
    try { await post('/api/session', { token: $('access-key').value.trim() }); await showApp(); }
    catch (error) { setError('login-error', error.message); }
    finally { submit.disabled = false; submit.textContent = '기록장 열기 ↗'; }
  });
  $('logout-button').addEventListener('click', async () => {
    try { await api('/api/session', { method: 'DELETE' }); showLogin(); }
    catch (error) { toast(error.message, true); }
  });

  async function refresh(force = false) {
    if (!state.authenticated || state.refreshBusy) return;
    state.refreshBusy = true;
    try {
      const fetchEntries = force || Date.now() - state.lastEntries > 5000;
      const results = await Promise.allSettled([
        api('/api/jobs'),
        fetchEntries ? api('/api/entries') : Promise.resolve(null),
      ]);
      if (!state.authenticated) return;
      if (results[0].status === 'fulfilled') {
        const jobs = results[0].value.jobs || [];
        const json = JSON.stringify(jobs);
        if (json !== state.jobJSON) {
          const newlyCompleted = jobs.some((job) => job.status === 'completed' && state.jobs.some((old) => old.id === job.id && old.status !== 'completed'));
          state.jobs = jobs; state.jobJSON = json; renderJobs();
          if (newlyCompleted) state.lastEntries = 0;
        }
      }
      if (results[1].status === 'fulfilled' && results[1].value) {
        const entries = results[1].value.entries || [];
        const json = JSON.stringify(entries);
        if (json !== state.entryJSON) {
          state.entries = entries.sort((a, b) => b.date.localeCompare(a.date) || a.id.localeCompare(b.id));
          state.entryJSON = json;
          if (!state.calendarInitialized && entries.length) {
            [state.year, state.month] = state.entries[0].date.split('-').map(Number);
            state.calendarInitialized = true;
          }
          if (state.target) state.target = state.entries.find((entry) => entry.id === state.target.id) || null;
          renderTarget(); renderFeed(); renderCalendar(); renderMeta();
        }
        state.lastEntries = Date.now(); setError('feed-error', '');
      } else if (results[1].status === 'rejected') {
        setError('feed-error', results[1].reason.message);
      }
      if (results[0].status === 'rejected' && results[1].status === 'fulfilled') {
        setError('feed-error', `작업 상태를 확인하지 못했어요. ${results[0].reason.message}`);
      }
    } finally { state.refreshBusy = false; }
  }
  setInterval(() => { if (document.visibilityState === 'visible') refresh(); }, 2000);
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') refresh(true); });

  function renderTarget() {
    const box = $('compose-target'); box.hidden = !state.target;
    if (state.target) box.querySelector('span').textContent = `${formatDate(state.target.date, true)} 기록에 이어 쓰기`;
    $('composer-heading').textContent = state.target ? '이날의 이야기를 더 남겨 주세요' : '어떤 순간을 남겨볼까요?';
  }
  $('compose-target').querySelector('button').addEventListener('click', () => { state.target = null; renderTarget(); $('compose-message').focus(); });
  function composeFor(entry) {
    state.target = entry; renderTarget();
    $('compose-message').focus({ preventScroll: true });
    $('compose-form').scrollIntoView({ behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth', block: 'center' });
  }
  async function submitJob(message, entryID = '') {
    const requestKey = `${entryID}\0${message}`;
    const requestID = state.pendingRequests.get(requestKey) || crypto.randomUUID();
    state.pendingRequests.set(requestKey, requestID);
    const result = await post('/api/jobs', { message, entry_id: entryID, request_id: requestID });
    state.pendingRequests.delete(requestKey);
    if (result.job) {
      state.jobs = [result.job, ...state.jobs.filter((j) => j.id !== result.job.id)];
      state.jobJSON = JSON.stringify(state.jobs); renderJobs();
    }
    return result;
  }
  $('compose-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    const input = $('compose-message'); const message = input.value.trim();
    if (!message) { input.focus(); return; }
    const submit = $('compose-submit'); submit.disabled = true;
    setError('compose-error', '');
    try {
      await submitJob(message, state.target?.id || '');
      input.value = ''; state.target = null; renderTarget(); toast('이야기를 받았어요. 차근차근 정리할게요.');
    } catch (error) { setError('compose-error', error.message); }
    finally { submit.disabled = false; }
  });

  function renderJobs() {
    const focus = document.activeElement;
    const focusJobID = focus?.dataset?.answerFor;
    const selection = focusJobID ? [focus.selectionStart, focus.selectionEnd] : null;
    const jobs = [...state.jobs].sort((a, b) => (b.created_at || '').localeCompare(a.created_at || ''));
    const visible = jobs.filter((job) => !state.dismissed.has(job.id) && (['queued', 'running', 'saving', 'needs_input', 'failed'].includes(job.status) || jobs.indexOf(job) < 2));
    const fragment = document.createDocumentFragment();
    visible.forEach((job) => {
      const status = Object.hasOwn(statusLabels, job.status) ? job.status : 'running';
      const card = el('article', `job-card ${status}`);
      const top = el('div', 'job-top'); top.append(el('span', 'status-dot'), el('h3', '', statusLabels[status]));
      if (['completed', 'cancelled', 'failed'].includes(status)) {
        const dismiss = button('×', 'icon-button tiny', () => { state.dismissed.add(job.id); renderJobs(); });
        dismiss.setAttribute('aria-label', '정리 알림 닫기'); top.append(dismiss);
      }
      card.append(top);
      const text = job.error || job.summary || job.message;
      if (text) card.append(el('p', 'job-message', text.length > 300 ? `${text.slice(0, 300)}…` : text));
      if (status === 'needs_input') {
        const questions = el('ul', 'job-questions');
        (job.questions || []).forEach((q) => questions.append(el('li', '', typeof q === 'string' ? q : q.question || q.title || '조금 더 자세히 알려 주세요.')));
        card.append(questions);
        const form = el('form', 'job-answer');
        const answer = el('textarea'); answer.rows = 2; answer.required = true; answer.maxLength = 20000;
        answer.placeholder = '기억나는 내용을 알려 주세요'; answer.setAttribute('aria-label', '추가 질문에 대한 답변'); answer.dataset.answerFor = job.id;
        answer.value = state.answers.get(job.id) || '';
        answer.addEventListener('input', () => state.answers.set(job.id, answer.value));
        const send = button('답변 보내기', 'button primary'); send.type = 'submit';
        const err = el('p', 'form-error'); err.hidden = true; err.setAttribute('role', 'alert');
        form.append(answer, send);
        form.addEventListener('submit', async (event) => {
          event.preventDefault(); if (!answer.value.trim()) return;
          send.disabled = true; err.hidden = true;
          try { await post(`/api/jobs/${encodeURIComponent(job.id)}/answers`, { message: answer.value.trim() }); state.answers.delete(job.id); await refresh(true); }
          catch (error) { err.textContent = error.message; err.hidden = false; }
          finally { send.disabled = false; }
        });
        card.append(form, err);
      }
      if (['queued', 'running', 'needs_input', 'failed', 'cancelled'].includes(status)) {
        const actions = el('div', 'job-actions');
        if (['queued', 'running', 'needs_input'].includes(status)) {
          actions.append(button('정리 멈추기', 'button quiet', async (event) => {
            event.currentTarget.disabled = true;
            try { await post(`/api/jobs/${encodeURIComponent(job.id)}/cancel`); await refresh(true); }
            catch (error) { toast(error.message, true); event.target.disabled = false; }
          }));
        } else if (job.message) {
          actions.append(button('다시 정리하기', 'button secondary', async (event) => {
            event.currentTarget.disabled = true;
            try { await post(`/api/jobs/${encodeURIComponent(job.id)}/retry`); await refresh(true); }
            catch (error) { toast(error.message, true); event.target.disabled = false; }
          }));
        }
        card.append(actions);
      }
      fragment.append(card);
    });
    $('jobs').replaceChildren(fragment);
    if (focusJobID) {
      const next = [...$('jobs').querySelectorAll('textarea')].find((input) => input.dataset.answerFor === focusJobID);
      if (next) { next.focus({ preventScroll: true }); next.setSelectionRange(...selection); }
    }
  }

  function renderBody(body, node) {
    const regex = /\[([^\]\n]+)\]\(([^)\n]+)\)/g;
    let cursor = 0; let match;
    while ((match = regex.exec(body))) {
      node.append(document.createTextNode(body.slice(cursor, match.index)));
      const label = match[1]; const raw = match[2]; const relative = raw.replace(/^\.\//, '');
      if (/^[^/\\]+\.txt$/.test(relative) && !relative.startsWith('.')) {
        const link = button(label, 'place-link', () => showPlace(relative));
        node.append(link);
      } else if (/^[^/\\]+\.md$/.test(relative) && !relative.startsWith('.')) {
        const link = button(label, 'entry-link', () => {
          const entry = state.entries.find((e) => e.id === relative);
          if (entry) { state.selectedDate = entry.date; state.query = ''; $('search').value = ''; renderFeed(); renderCalendar(); }
          else toast('연결된 기록을 찾지 못했어요.', true);
        });
        node.append(link);
      } else {
        let url; try { url = new URL(raw); } catch { /* Preserve unsupported links as text. */ }
        if (url && ['http:', 'https:'].includes(url.protocol)) {
          const link = el('a', '', label); link.href = url.href; link.target = '_blank'; link.rel = 'noopener noreferrer'; node.append(link);
        } else node.append(document.createTextNode(match[0]));
      }
      cursor = regex.lastIndex;
    }
    node.append(document.createTextNode(body.slice(cursor)));
  }
  function renderFeed() {
    const query = state.query.trim().toLocaleLowerCase('ko');
    const entries = state.entries.filter((entry) => (!state.selectedDate || entry.date === state.selectedDate) && (!query || `${entry.title} ${entry.date} ${entry.body}`.toLocaleLowerCase('ko').includes(query)));
    $('entry-count').textContent = String(entries.length);
    $('feed-heading').textContent = query ? '찾은 기록' : state.selectedDate ? '이날의 기록' : '모든 기록';
    $('active-filter').hidden = !state.selectedDate;
    if (state.selectedDate) $('active-filter').querySelector('span').textContent = formatDate(state.selectedDate);
    const fragment = document.createDocumentFragment();
    let previousMonth = '';
    entries.forEach((entry) => {
      const [year, month, day] = entry.date.split('-').map(Number);
      const monthKey = `${year}-${month}`;
      if (monthKey !== previousMonth) {
        fragment.append(el('h3', 'month-divider', `${year}년 ${month}월`)); previousMonth = monthKey;
      }
      const article = el('article', 'entry');
      const header = el('div', 'entry-header');
      const stamp = el('div', 'date-stamp'); stamp.setAttribute('aria-hidden', 'true');
      stamp.append(el('span', 'day', String(day).padStart(2, '0')), el('span', 'weekday', weekdays[new Date(year, month - 1, day).getDay()]));
      const titles = el('div', 'entry-title-group'); const title = el('h3', 'entry-title', prettyTitle(entry));
      const date = el('time', 'entry-date', formatDate(entry.date)); date.dateTime = entry.date;
      titles.append(title, date); header.append(stamp, titles); article.append(header);
      const long = entry.body.split('\n').length > 6 || entry.body.length > 340;
      const collapsed = long && !state.expanded.has(entry.id);
      const body = el('div', `entry-body${collapsed ? ' collapsed' : ''}`); renderBody(entry.body, body); article.append(body);
      if (long) {
        const expand = button(collapsed ? '이야기 더 보기' : '접기', 'expand-button', () => {
          if (state.expanded.has(entry.id)) state.expanded.delete(entry.id); else state.expanded.add(entry.id);
          body.classList.toggle('collapsed', !state.expanded.has(entry.id));
          expand.textContent = state.expanded.has(entry.id) ? '접기' : '이야기 더 보기';
          expand.setAttribute('aria-expanded', String(state.expanded.has(entry.id)));
        });
        expand.setAttribute('aria-expanded', String(!collapsed)); article.append(expand);
      }
      const footer = el('div', 'entry-footer');
      const add = button('', 'button', () => composeFor(entry)); add.append(icon('add'), document.createTextNode('이야기 덧붙이기'));
      const edit = button('', 'button edit-button', () => showEditor(entry)); edit.append(icon('edit'), document.createTextNode('직접 수정'));
      footer.append(add, edit); article.append(footer); fragment.append(article);
    });
    $('feed').replaceChildren(fragment);
    $('empty-state').hidden = entries.length > 0;
    $('empty-state').querySelector('h3').textContent = query ? '아직 이 장면은 찾지 못했어요' : state.selectedDate ? '이날의 페이지는 아직 비어 있어요' : '첫 페이지를 기다리고 있어요';
    $('empty-state').querySelector('p').textContent = query ? '다른 단어나 날짜로 다시 찾아볼까요?' : state.selectedDate ? '기억나는 일이 있다면 날짜와 함께 이야기를 남겨 주세요.' : '함께한 날의 이야기를 위에 남겨 주세요.';
    $('feed-end').hidden = !entries.length;
  }
  $('search').addEventListener('input', (event) => { state.query = event.target.value; renderFeed(); });
  $('clear-filter').addEventListener('click', () => { state.selectedDate = ''; renderFeed(); renderCalendar(); });
  function renderMeta() {
    if (!state.entries.length) { $('journal-meta').replaceChildren(); return; }
    const first = state.entries[state.entries.length - 1];
    $('journal-meta').replaceChildren(el('p', '', `${formatDate(first.date, true)}부터`), el('p', '', `${state.entries.length}개의 이야기를 모았어요.`));
  }
  function renderCalendar() {
    $('calendar-title').textContent = `${state.year}년 ${state.month}월`;
    const firstWeekday = new Date(state.year, state.month - 1, 1).getDay();
    const daysInMonth = new Date(state.year, state.month, 0).getDate();
    const cells = Math.ceil((firstWeekday + daysInMonth) / 7) * 7;
    const counts = new Map(); state.entries.forEach((entry) => counts.set(entry.date, (counts.get(entry.date) || 0) + 1));
    const fragment = document.createDocumentFragment();
    for (let i = 0; i < cells; i++) {
      const date = new Date(state.year, state.month - 1, 1 - firstWeekday + i);
      const dateString = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
      const outside = date.getMonth() + 1 !== state.month; const count = counts.get(dateString) || 0;
      const day = button(String(date.getDate()), `calendar-day${outside ? ' outside' : ''}${i % 7 === 0 ? ' sunday' : ''}${dateString === today ? ' today' : ''}${count ? ' has-entries' : ''}${dateString === state.selectedDate ? ' selected' : ''}`, () => {
        state.selectedDate = state.selectedDate === dateString ? '' : dateString;
        if (outside) { state.year = date.getFullYear(); state.month = date.getMonth() + 1; }
        renderCalendar(); renderFeed();
      });
      day.setAttribute('aria-label', `${formatDate(dateString)}${count ? `, 기록 ${count}개` : ', 기록 없음'}${dateString === today ? ', 오늘' : ''}`);
      day.setAttribute('aria-pressed', String(dateString === state.selectedDate));
      if (dateString === today) day.setAttribute('aria-current', 'date');
      fragment.append(day);
    }
    $('calendar-grid').replaceChildren(fragment);
  }
  function changeMonth(delta) { const d = new Date(state.year, state.month - 1 + delta, 1); state.year = d.getFullYear(); state.month = d.getMonth() + 1; renderCalendar(); }
  $('calendar-prev').addEventListener('click', () => changeMonth(-1));
  $('calendar-next').addEventListener('click', () => changeMonth(1));
  $('calendar-today').addEventListener('click', () => { [state.year, state.month] = today.split('-').map(Number); state.selectedDate = ''; renderCalendar(); renderFeed(); });
  $('mobile-calendar-toggle').addEventListener('click', () => { const open = $('calendar-panel').classList.toggle('is-open'); $('mobile-calendar-toggle').setAttribute('aria-expanded', String(open)); });

  function openModal(title) {
    const version = ++state.modalVersion;
    $('modal-title').textContent = title; $('modal-body').replaceChildren();
    if (!$('modal').open) $('modal').showModal();
    return version;
  }
  function closeModal() { state.modalVersion++; if ($('modal').open) $('modal').close(); }
  $('modal-close').addEventListener('click', closeModal);
  $('modal').addEventListener('cancel', () => { state.modalVersion++; });
  $('modal').addEventListener('click', (event) => {
    if (event.target !== $('modal')) return;
    const rect = $('modal').getBoundingClientRect();
    if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) closeModal();
  });
  async function showPlace(id) {
    const version = openModal('장소'); $('modal-body').append(el('p', 'modal-description', '장소를 찾아보고 있어요…'));
    try {
      const place = await api(`/api/places/${encodeURIComponent(id)}`);
      if (version !== state.modalVersion) return;
      $('modal-title').textContent = place.name || id.replace(/\.txt$/, '');
      const address = el('p', 'place-address'); address.append(icon('pin'), document.createTextNode(place.address || '아직 주소가 기록되지 않았어요.'));
      $('modal-body').replaceChildren(address);
      let url; try { url = new URL(place.url); } catch { /* Missing map URL is allowed. */ }
      if (url && ['http:', 'https:'].includes(url.protocol)) {
        const link = el('a', 'button primary map-link', '지도에서 보기 ↗'); link.href = url.href; link.target = '_blank'; link.rel = 'noopener noreferrer'; $('modal-body').append(link);
      }
    } catch (error) { if (version === state.modalVersion) $('modal-body').replaceChildren(el('p', 'form-error', error.message)); }
  }
  function showEditor(entry) {
    openModal('기록 직접 수정');
    const form = el('form'); const label = el('label', 'source-label', `${formatDate(entry.date, true)} · ${prettyTitle(entry)}`); label.htmlFor = 'edit-body';
    const textarea = el('textarea', 'edit-textarea'); textarea.id = 'edit-body'; textarea.value = entry.body; textarea.required = true; textarea.maxLength = 100000;
    const error = el('p', 'form-error'); error.hidden = true; error.setAttribute('role', 'alert');
    const actions = el('div', 'modal-actions'); const cancel = button('취소', 'button quiet', closeModal); const save = button('수정 내용 저장', 'button primary'); save.type = 'submit'; actions.append(cancel, save);
    form.append(label, textarea, error, actions);
    form.addEventListener('submit', async (event) => {
      event.preventDefault(); save.disabled = true; error.hidden = true;
      try { await post(`/api/entries/${encodeURIComponent(entry.id)}`, { body: textarea.value, base_hash: entry.hash }); closeModal(); toast('수정한 내용을 저장했어요.'); state.lastEntries = 0; await refresh(true); }
      catch (err) { error.textContent = err.message; error.hidden = false; }
      finally { save.disabled = false; }
    });
    $('modal-body').append(form); textarea.focus();
  }
  async function showHistory() {
    const version = openModal('우리 기록의 변경 이력'); $('modal-body').append(el('p', 'modal-description', '지난 변경을 불러오고 있어요…'));
    try {
      const data = await api('/api/history'); if (version !== state.modalVersion) return;
      const changes = data.changes || [];
      if (!changes.length) { $('modal-body').replaceChildren(el('p', 'modal-description', '아직 변경 이력이 없어요.')); return; }
      const list = el('div', 'history-list');
      changes.forEach((change) => {
        const row = button('', 'history-row', () => showChange(change)); const text = el('div');
        text.append(el('h3', '', changeLabel(change)), el('p', '', formatTime(change.timestamp)));
        row.append(text, el('span', '', '›')); list.append(row);
      });
      $('modal-body').replaceChildren(el('p', 'modal-description', '기록이 어떻게 달라졌는지 살펴볼 수 있어요.'), list);
    } catch (error) { if (version === state.modalVersion) $('modal-body').replaceChildren(el('p', 'form-error', error.message)); }
  }
  async function showChange(change) {
    const version = openModal('변경 내용'); $('modal-body').append(el('p', 'modal-description', '변경 내용을 불러오고 있어요…'));
    try {
      const data = await api(`/api/history/${encodeURIComponent(change.id)}`); if (version !== state.modalVersion) return;
      const diff = el('pre', 'diff-view');
      (data.diff || '변경된 내용이 없어요.').split('\n').forEach((line) => {
        const style = line.startsWith('+') && !line.startsWith('+++') ? ' add' : line.startsWith('-') && !line.startsWith('---') ? ' remove' : line.startsWith('@@') ? ' meta' : '';
        diff.append(el('span', `diff-line${style}`, line || ' '));
      });
      const actions = el('div', 'modal-actions'); const back = button('이력 목록', 'button quiet', showHistory);
      const revert = button('이 변경 되돌리기', 'button danger', () => confirmRevert(change));
      actions.append(back, revert);
      $('modal-body').replaceChildren(el('p', 'modal-description', changeLabel(change)), diff, actions);
    } catch (error) { if (version === state.modalVersion) $('modal-body').replaceChildren(el('p', 'form-error', error.message), button('이력 목록', 'button secondary', showHistory)); }
  }
  function confirmRevert(change) {
    openModal('이 변경을 되돌릴까요?');
    const error = el('p', 'form-error'); error.hidden = true; error.setAttribute('role', 'alert');
    const actions = el('div', 'modal-actions'); const back = button('취소', 'button quiet', () => showChange(change));
    const confirm = button('되돌리기', 'button danger', async () => {
      confirm.disabled = true; back.disabled = true; error.hidden = true;
      try { await post(`/api/history/${encodeURIComponent(change.id)}/revert`); closeModal(); toast('선택한 변경을 되돌렸어요.'); state.lastEntries = 0; await refresh(true); }
      catch (err) { error.textContent = err.message; error.hidden = false; }
      finally { confirm.disabled = false; back.disabled = false; }
    });
    actions.append(back, confirm);
    $('modal-body').append(el('p', 'modal-description', `${changeLabel(change)}\n\n이 변경을 취소하는 새 이력이 남아요. 이후 수정과 충돌하면 원본을 유지하고 알려드릴게요.`), error, actions);
  }
  $('history-button').addEventListener('click', showHistory);

  renderCalendar(); renderFeed();
  api('/api/session').then((data) => data.authenticated ? showApp() : showLogin()).catch((error) => { showLogin(); setError('login-error', error.message); });
})();
