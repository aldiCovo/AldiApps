"use strict";

// The Chat page: customers' conversations, newest first, and the one being answered. Admins
// get no live signal, so the page asks again every few seconds while it is open, and the menu
// badge every half minute everywhere else.

const chatPollOpen = 5000;
const chatPollBadge = 30000;
const chatPageSize = 20;
const chatShortTime = new Intl.DateTimeFormat("id-ID", { hour: "2-digit", minute: "2-digit" });
const chatShortDay = new Intl.DateTimeFormat("id-ID", { day: "numeric", month: "short" });
const chatDay = new Intl.DateTimeFormat("id-ID", { weekday: "long", day: "numeric", month: "long", year: "numeric" });
const chatMonth = new Intl.DateTimeFormat("id-ID", { month: "long", year: "numeric" });
const orderStatusShort = {
  pending: "Diproses", awaiting_payment: "Menunggu bayar", paying: "Dibayar", paid: "Perlu dikemas", packed: "Dikemas",
  shipped: "Dikirim", arrived: "Sampai", completed: "Selesai", rejected: "Ditolak", cancelled: "Dibatalkan",
};

const chat = {
  filter: "waiting",
  list: [],
  total: 0,
  // The customer whose conversation is open, and what is shown of it.
  userId: null,
  messages: [],
  hasMore: false,
  readUpTo: 0,
  timer: 0,
  badgeTimer: 0,
  request: 0,
  sending: false,
  // The product card that goes with the next reply, and the catalog the picker searches.
  pending: null,
  catalog: null,
  // Product id -> promise of its live stock line, kept while the page is open.
  stock: new Map(),
  // The shared quick replies, and the open customer's first name for {nama}.
  quick: null,
  firstName: "",
  // Where the open conversation stands with the bot, and the shop's bot settings.
  state: null,
  bot: null,
  // A conversation to open once the list is in, from a #chat-<id> link.
  openId: null,
};
const quickMessages = {
  "there can be at most 30 quick replies": "Sudah ada 30 balasan cepat. Hapus satu dulu.",
  "body is required": "Isi balasannya dulu.",
  "body must be at most 500 characters": "Balasan cepat maksimal 500 huruf.",
  "quick reply not found": "Balasan itu sudah dihapus admin lain.",
};

async function loadQuickReplies() {
  try {
    const { quick_replies: list } = await api("/chat/quick-replies");
    chat.quick = list;
  } catch (error) {
    if (handleAuth(error)) return;
    chat.quick = chat.quick || [];
  }
  renderQuickChips();
  if (!$("chat-quick-editor").hidden) renderQuickEditor();
}

function quickText(body) {
  return body.replaceAll("{nama}", chat.firstName || "Kakak");
}

// The chips above the reply box. A chip fills the box so the reply can still be changed.
function renderQuickChips() {
  const chips = (chat.quick || []).map((reply) => {
    const chip = button(quickText(reply.body), "chat-chip", () => {
      const input = $("chat-input");
      const text = quickText(reply.body);
      input.value = input.value.trim() ? `${input.value.trimEnd()} ${text}` : text;
      input.focus();
      input.setSelectionRange(input.value.length, input.value.length);
    });
    chip.title = quickText(reply.body);
    return chip;
  });
  const manage = button(chips.length > 0 ? "Atur" : "Buat balasan cepat", "link chat-chip-manage", openQuickEditor);
  $("chat-quick").replaceChildren(...chips, manage);
}

function openQuickEditor() {
  closeChatPicker();
  $("chat-quick-editor").hidden = false;
  $("chat-quick-status").textContent = "";
  renderQuickEditor();
  $("chat-quick-new-text").focus();
}

function closeQuickEditor() {
  $("chat-quick-editor").hidden = true;
}

function renderQuickEditor() {
  $("chat-quick-rows").replaceChildren(...(chat.quick || []).map((reply) => {
    const row = el("div", "chat-quick-row");
    const text = el("textarea");
    text.rows = 2;
    text.maxLength = 500;
    text.value = reply.body;
    text.setAttribute("aria-label", "Balasan cepat");
    const save = button("Simpan", "secondary", () => changeQuick(() => send("PUT", `/chat/quick-replies/${reply.id}`, { body: text.value }), "Disimpan."));
    const remove = button("Hapus", "link danger", () => {
      if (remove.dataset.sure !== "1") {
        remove.dataset.sure = "1";
        remove.textContent = "Yakin hapus?";
        return;
      }
      changeQuick(() => send("DELETE", `/chat/quick-replies/${reply.id}`), "Dihapus.");
    });
    const actions = el("div", "chat-quick-actions");
    actions.append(save, remove);
    row.append(text, actions);
    return row;
  }));
}

async function changeQuick(request, done) {
  $("chat-quick-status").textContent = "Menyimpan…";
  try {
    await request();
    $("chat-quick-status").textContent = done;
    await loadQuickReplies();
    return true;
  } catch (error) {
    if (handleAuth(error)) return false;
    $("chat-quick-status").textContent = quickMessages[error.message] || "Belum tersimpan. Coba lagi.";
    return false;
  }
}
const chatOrderStatus = {
  pending: "Diproses", awaiting_payment: "Menunggu bayar", paying: "Dibayar", paid: "Dibayar", packed: "Dikemas",
  shipped: "Dikirim", arrived: "Sampai", completed: "Selesai", rejected: "Ditolak", cancelled: "Dibatalkan",
};

// What the list shows for a message that is only a card.
function cardSummary(card) {
  return card.kind === "product" ? `Produk: ${card.name}` : `Pesanan #${card.id}`;
}

function cardPhoto(card) {
  if (card.kind !== "product") return "";
  const own = (productPhotos[card.id] || []).find(Boolean);
  return own || categoryPhotos[card.category_id] || "";
}

// The stock of each size and colour now, so the admin answers from what is really left.
function liveStock(id) {
  if (!chat.stock.has(id)) {
    chat.stock.set(id, api(`/products/${id}`).then((product) => {
      const variants = product.variants || [];
      if (variants.length === 0) return "Belum ada varian";
      return "Stok sekarang: " + variants.map((v) => `${v.size} ${v.color} ${v.stock}`).join(" · ");
    }, (error) => (error.status === 404 ? "Produk ini sudah dihapus" : "")));
  }
  return chat.stock.get(id);
}

// A product or order card, in a message, the picker, or waiting to be sent.
function chatCard(card, onClick) {
  const box = el(onClick ? "button" : "div", `chat-card ${card.kind}`);
  if (onClick) {
    box.type = "button";
    box.addEventListener("click", onClick);
  }
  const thumb = el("span", "chat-card-thumb");
  thumb.setAttribute("aria-hidden", "true");
  const url = cardPhoto(card);
  if (url) {
    const image = el("img");
    image.src = url;
    image.alt = "";
    image.loading = "lazy";
    image.addEventListener("error", () => image.remove());
    thumb.append(image);
  } else if (card.kind === "order") {
    thumb.textContent = "#";
  }
  const text = el("span", "chat-card-text");
  if (card.kind === "product") {
    text.append(el("strong", "", card.name), el("span", "", `${rupiah.format(card.price)} · #${card.id}`));
  } else {
    text.append(el("strong", "", `Pesanan #${card.id}`),
      el("span", "", `${chatOrderStatus[card.status] || card.status} saat ditanya · ${rupiah.format(card.total)}`));
    if (card.name) text.append(el("span", "", card.more > 0 ? `${card.name} dan ${card.more} lainnya` : card.name));
  }
  box.append(thumb, text);
  return box;
}

function setChatPending(card) {
  chat.pending = card;
  const box = $("chat-pending");
  if (!card) {
    box.hidden = true;
    box.replaceChildren();
    return;
  }
  box.replaceChildren(chatCard(card), button("Hapus", "link", () => {
    setChatPending(null);
    $("chat-input").focus();
  }));
  box.hidden = false;
}

async function openChatPicker() {
  closeQuickEditor();
  $("chat-picker").hidden = false;
  $("chat-attach").setAttribute("aria-expanded", "true");
  $("chat-pick-search").value = "";
  $("chat-pick-search").focus();
  if (!chat.catalog) {
    $("chat-pick-list").replaceChildren(el("p", "muted small", "Memuat produk…"));
    try {
      const [page] = await Promise.all([api("/products?limit=100"), photosLoaded ? null : loadPhotoLists()]);
      chat.catalog = page.products;
    } catch (error) {
      if (!handleAuth(error)) $("chat-pick-list").replaceChildren(el("p", "muted small", "Produk belum bisa dimuat."));
      return;
    }
  }
  showChatPicks();
}

function showChatPicks() {
  const words = $("chat-pick-search").value.trim().toLowerCase();
  const found = chat.catalog.filter((product) => product.name.toLowerCase().includes(words)).slice(0, 30);
  $("chat-pick-list").replaceChildren(...(found.length > 0 ? found.map((product) => {
    const card = { kind: "product", id: product.id, name: product.name, price: product.price, category_id: product.category_id };
    return chatCard(card, () => {
      setChatPending(card);
      closeChatPicker();
      $("chat-input").focus();
    });
  }) : [el("p", "muted small", "Tidak ada produk dengan nama itu.")]));
}

function closeChatPicker() {
  $("chat-picker").hidden = true;
  $("chat-attach").setAttribute("aria-expanded", "false");
}

function chatWhen(iso) {
  const date = new Date(iso);
  return date.toDateString() === new Date().toDateString() ? chatShortTime.format(date) : chatShortDay.format(date);
}

function customerName(conversation) {
  return conversation.customer ? conversation.customer.name : `Akun #${conversation.user_id} (sudah dihapus)`;
}

function setChatBadge(waiting) {
  const badge = $("chat-badge");
  badge.hidden = !(waiting > 0);
  badge.textContent = waiting > 99 ? "99+" : String(waiting || "");
  badge.setAttribute("aria-label", `${waiting} perlu admin`);
}

// The badge on the menu, on every page.
function watchChatBadge() {
  clearInterval(chat.badgeTimer);
  const check = () => {
    if (!$("chat-view").hidden || document.hidden) return;
    api("/chat/waiting").then(({ waiting }) => setChatBadge(waiting), () => {});
  };
  check();
  chat.badgeTimer = setInterval(check, chatPollBadge);
}

function openChat() {
  for (const filter of ["waiting", "bot", "all"]) {
    $(`chat-tab-${filter}`).setAttribute("aria-pressed", String(chat.filter === filter));
  }
  refreshChat(true).then(() => {
    if (chat.openId === null) return;
    const id = chat.openId;
    chat.openId = null;
    openConversation(chat.list.find((c) => c.user_id === id) || { user_id: id, customer: null });
  });
  loadBotSettings();
  if (!chat.quick) loadQuickReplies();
  clearInterval(chat.timer);
  chat.timer = setInterval(() => { if (!document.hidden) refreshChat(false); }, chatPollOpen);
}

function leaveChat() {
  clearInterval(chat.timer);
  chat.timer = 0;
}

async function refreshChat(showLoading) {
  await loadChatList(showLoading, Math.max(chat.list.length, chatPageSize));
  if (chat.userId !== null) await loadChatMessages(false);
}

async function loadChatList(showLoading, limit = chatPageSize, offset = 0) {
  const request = ++chat.request;
  if (showLoading && chat.list.length === 0) $("chat-list-status").textContent = "Memuat…";
  let page;
  try {
    page = await api(`/chat/conversations?filter=${chat.filter}&limit=${Math.min(limit, 100)}&offset=${offset}`);
  } catch (error) {
    if (handleAuth(error)) return;
    if (request === chat.request) $("chat-list-status").textContent = "Chat belum bisa dimuat. Coba lagi sebentar lagi.";
    return;
  }
  if (request !== chat.request) return;
  chat.list = offset === 0 ? page.conversations : [...chat.list, ...page.conversations];
  chat.total = page.total;
  setChatBadge(page.waiting);
  $("chat-tab-waiting").textContent = `Perlu admin (${page.waiting})`;
  renderChatList();
}

function renderChatList() {
  $("chat-list").replaceChildren(...chat.list.map(chatRow));
  $("chat-more").hidden = chat.list.length >= chat.total;
  $("chat-list-status").textContent = chat.list.length > 0 ? ""
    : chat.filter === "waiting" ? "Tidak ada chat yang perlu admin."
    : chat.filter === "bot" ? "Belum ada chat yang dijawab bot." : "Belum ada pelanggan yang chat.";
}

function chatRow(conversation) {
  const item = el("li");
  const row = el("button", "chat-row");
  row.type = "button";
  if (conversation.unread > 0) row.classList.add("unread");
  if (conversation.user_id === chat.userId) row.setAttribute("aria-current", "true");
  const head = el("span", "chat-row-head");
  head.append(el("strong", "", customerName(conversation)), el("span", "muted small", chatWhen(conversation.last_message.created_at)));
  const last = el("span", "chat-row-last");
  const lastMessage = conversation.last_message;
  const said = lastMessage.body || (lastMessage.attachment ? cardSummary(lastMessage.attachment) : "");
  const text = ({ admin: "Admin: ", bot: "Bot: " }[lastMessage.from] || "") + said;
  last.append(el("span", "chat-row-text", text));
  if (conversation.unread > 0) last.append(el("span", "chat-count", String(conversation.unread)));
  row.append(head, last);
  if (conversation.waiting) row.append(el("span", "chat-row-reason", conversation.reason || "Perlu admin"));
  else if (conversation.bot_off) row.append(el("span", "chat-row-reason", "Ditangani admin"));
  row.addEventListener("click", () => openConversation(conversation));
  item.append(row);
  return item;
}

async function openConversation(conversation) {
  chat.userId = conversation.user_id;
  chat.messages = [];
  chat.hasMore = false;
  chat.readUpTo = 0;
  chat.state = null;
  renderBotState();
  history.replaceState(null, "", `#chat-${conversation.user_id}`);
  $("chat-view").classList.add("thread-open");
  $("chat-empty").hidden = true;
  $("chat-open").hidden = false;
  $("chat-who").textContent = customerName(conversation);
  chat.firstName = conversation.customer ? conversation.customer.name.trim().split(/\s+/)[0] : "";
  closeQuickEditor();
  renderQuickChips();
  $("chat-who-email").textContent = conversation.customer ? conversation.customer.email : "";
  $("chat-lines").replaceChildren();
  $("chat-error").hidden = true;
  $("chat-input").value = "";
  setChatPending(null);
  closeChatPicker();
  chat.stock.clear();
  if (!photosLoaded) loadPhotoLists().then(() => { if (chat.userId === conversation.user_id) renderChatMessages(); });
  renderChatList();
  loadChatInfo(conversation.user_id);
  await loadChatMessages(true);
  $("chat-input").focus();
}

function closeConversation() {
  chat.userId = null;
  history.replaceState(null, "", "#chat");
  $("chat-view").classList.remove("thread-open");
  $("chat-open").hidden = true;
  $("chat-info").hidden = true;
  $("chat-empty").hidden = false;
  renderChatList();
}

async function loadChatInfo(userId) {
  $("chat-info").hidden = true;
  let info;
  try {
    info = await api(`/chat/conversations/${userId}`);
  } catch {
    return;
  }
  if (chat.userId !== userId) return;
  if (info.customer) {
    $("chat-who").textContent = info.customer.name;
    $("chat-who-email").textContent = info.customer.email;
    chat.firstName = info.customer.name.trim().split(/\s+/)[0];
    renderQuickChips();
  }
  $("chat-info-name").textContent = info.customer ? `${info.customer.name} · ${info.customer.email}` : "Akunnya sudah dihapus.";
  $("chat-info-since").textContent = info.customer && info.customer.created_at
    ? `Member sejak ${chatMonth.format(new Date(info.customer.created_at))}` : "";
  $("chat-info-orders").replaceChildren(...(info.orders.length > 0 ? info.orders.map((order) => {
    const line = el("li");
    line.append(el("strong", "", `#${order.id}`), el("span", "pill-small", orderStatusShort[order.status] || order.status), el("span", "muted small", rupiah.format(order.total)));
    return line;
  }) : [el("li", "muted small", "Belum pernah pesan.")]));
  $("chat-info").hidden = false;
}

// Loads the newest messages, or older ones before what is shown.
async function loadChatMessages(scrollDown, older = false) {
  const userId = chat.userId;
  const before = older && chat.messages.length > 0 ? `&before=${chat.messages[0].id}` : "";
  let page;
  try {
    page = await api(`/chat/conversations/${userId}/messages?limit=50${before}`);
  } catch (error) {
    if (handleAuth(error) || chat.userId !== userId) return;
    $("chat-error").textContent = "Pesan belum bisa dimuat. Coba lagi sebentar lagi.";
    $("chat-error").hidden = false;
    return;
  }
  if (chat.userId !== userId) return;
  const box = $("chat-messages");
  const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
  const oldHeight = box.scrollHeight;
  if (older) {
    chat.messages = [...page.messages, ...chat.messages];
    chat.hasMore = page.has_more;
  } else {
    // Older pages already loaded stay; the newest page replaces its own part.
    const first = page.messages.length > 0 ? page.messages[0].id : Infinity;
    const kept = chat.messages.filter((message) => message.id < first);
    const before = chat.messages.map((message) => message.id).join();
    chat.messages = [...kept, ...page.messages];
    if (kept.length === 0) chat.hasMore = page.has_more;
    const same = before === chat.messages.map((message) => message.id).join() && chat.readUpTo === page.read_up_to;
    chat.readUpTo = page.read_up_to;
    chat.state = { needsAdmin: page.needs_admin, botOff: page.bot_off, botOn: page.bot_on, working: page.bot_working };
    renderBotState();
    if (same && !scrollDown) return;
  }
  renderChatMessages();
  if (older) box.scrollTop = box.scrollHeight - oldHeight;
  else if (scrollDown || atBottom) box.scrollTop = box.scrollHeight;
  $("chat-error").hidden = true;
  if (!older && page.unread > 0) {
    send("POST", `/chat/conversations/${userId}/read`).then(() => loadChatList(false, Math.max(chat.list.length, chatPageSize)), () => {});
  }
}

function renderChatMessages() {
  $("chat-older").hidden = !chat.hasMore;
  const lines = [];
  let lastDay = "";
  const lastMine = chat.messages.findLast((message) => message.from !== "customer");
  for (const message of chat.messages) {
    const day = new Date(message.created_at).toDateString();
    if (day !== lastDay) {
      lines.push(el("p", "chat-day", chatDay.format(new Date(message.created_at))));
      lastDay = day;
    }
    const line = el("div", `chat-line ${message.from === "customer" ? "theirs" : "mine"}`);
    if (message.from === "bot") {
      line.classList.add("bot");
      line.append(el("span", "chat-from", "BOT · Asisten Prastika"));
    }
    if (message.attachment) {
      const card = message.attachment;
      line.classList.add("with-card");
      const shown = chatCard(card);
      if (card.kind === "product") {
        const stock = el("span", "chat-card-stock");
        shown.querySelector(".chat-card-text").append(stock);
        liveStock(card.id).then((text) => { stock.textContent = text; });
      }
      line.append(shown);
    }
    if (message.body) line.append(el("p", "chat-body", message.body));
    let meta = chatShortTime.format(new Date(message.created_at));
    if (message === lastMine) meta += message.id <= chat.readUpTo ? " · Dibaca" : " · Terkirim";
    line.append(el("span", "chat-meta", meta));
    lines.push(line);
  }
  $("chat-lines").replaceChildren(...lines);
}

async function sendChat() {
  const body = $("chat-input").value.trim();
  const card = chat.pending;
  if ((!body && !card) || chat.sending || chat.userId === null) return;
  const userId = chat.userId;
  chat.sending = true;
  $("chat-send").disabled = true;
  $("chat-error").hidden = true;
  try {
    const { message } = await post(`/chat/conversations/${userId}/messages`, card ? { body, attach: { kind: card.kind, id: card.id } } : { body });
    if (chat.userId === userId) {
      $("chat-input").value = "";
      if (chat.pending === card) setChatPending(null);
      if (!chat.messages.some((shown) => shown.id === message.id)) chat.messages.push(message);
      renderChatMessages();
      $("chat-messages").scrollTop = $("chat-messages").scrollHeight;
    }
    loadChatList(false, Math.max(chat.list.length, chatPageSize));
  } catch (error) {
    if (!handleAuth(error)) {
      $("chat-error").textContent = error.status === 422 ? "Produk itu sudah dihapus, jadi tidak bisa dilampirkan."
        : error.status === 400 ? "Balasan kosong atau lebih dari 1.000 huruf." : "Balasan belum terkirim. Coba lagi.";
      if (error.status === 422) {
        setChatPending(null);
        chat.catalog = null;
      }
      $("chat-error").hidden = false;
    }
  } finally {
    chat.sending = false;
    $("chat-send").disabled = false;
    $("chat-input").focus();
  }
}

$("chat-form").addEventListener("submit", (event) => {
  event.preventDefault();
  sendChat();
});
$("chat-input").addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    sendChat();
  }
});
$("chat-older").addEventListener("click", () => loadChatMessages(false, true));
$("chat-attach").addEventListener("click", () => ($("chat-picker").hidden ? openChatPicker() : closeChatPicker()));
$("chat-picker-close").addEventListener("click", () => {
  closeChatPicker();
  $("chat-attach").focus();
});
$("chat-quick-done").addEventListener("click", () => {
  closeQuickEditor();
  $("chat-input").focus();
});
$("chat-quick-new").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (await changeQuick(() => post("/chat/quick-replies", { body: $("chat-quick-new-text").value }), "Ditambahkan.")) {
    $("chat-quick-new-text").value = "";
  }
});
$("chat-pick-search").addEventListener("input", () => { if (chat.catalog) showChatPicks(); });
$("chat-pick-search").addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    closeChatPicker();
    $("chat-attach").focus();
  }
});
$("chat-back").addEventListener("click", closeConversation);
$("chat-more").addEventListener("click", () => loadChatList(false, chatPageSize, chat.list.length));
for (const [id, filter] of [["chat-tab-waiting", "waiting"], ["chat-tab-bot", "bot"], ["chat-tab-all", "all"]]) {
  $(id).addEventListener("click", () => {
    chat.filter = filter;
    chat.list = [];
    openChat();
  });
}
document.addEventListener("visibilitychange", () => {
  if (!document.hidden && !$("chat-view").hidden) refreshChat(false);
});

// Where the open conversation stands with the bot, and the button that changes it.
function renderBotState() {
  const state = chat.state;
  const label = $("chat-bot-state");
  const toggle = $("chat-bot-toggle");
  if (!state) {
    label.hidden = true;
    toggle.hidden = true;
    return;
  }
  let text = "";
  let kind = "";
  let action = null;
  if (state.working) {
    text = "Bot sedang mengetik…";
  } else if (state.botOff) {
    text = "Bot diam · admin yang menjawab";
    kind = "quiet";
    action = true;
  } else if (state.needsAdmin) {
    text = "Perlu admin";
    kind = "waiting";
    if (chat.bot && chat.bot.configured) action = true;
  } else if (state.botOn) {
    text = "Bot menjawab chat ini";
    action = false;
  }
  label.hidden = !text;
  label.textContent = text;
  label.className = `bot-state ${kind}`;
  toggle.hidden = action === null;
  toggle.textContent = action ? "Kembalikan ke bot" : "Ambil alih";
  toggle.dataset.on = String(action);
}

async function toggleBot() {
  const userId = chat.userId;
  const on = $("chat-bot-toggle").dataset.on === "true";
  $("chat-bot-toggle").disabled = true;
  try {
    await post(`/chat/conversations/${userId}/bot`, { on });
    if (chat.userId === userId) await loadChatMessages(false);
    loadChatList(false, Math.max(chat.list.length, chatPageSize));
  } catch (error) {
    if (!handleAuth(error)) {
      $("chat-error").textContent = "Belum berhasil. Coba lagi.";
      $("chat-error").hidden = false;
    }
  } finally {
    $("chat-bot-toggle").disabled = false;
  }
}

async function loadBotSettings() {
  try {
    chat.bot = await api("/chat/bot-settings");
  } catch (error) {
    handleAuth(error);
    return;
  }
  const pill = $("chat-bot-pill");
  const bot = chat.bot;
  const [text, kind] = !bot.configured ? ["Bot belum aktif", ""]
    : bot.mode === "off" ? ["Bot mati", ""]
    : bot.mode === "always" ? ["Bot aktif · menjawab dulu", "on"]
    : bot.answering_now ? ["Bot aktif · di luar jam kerja", "on"] : ["Bot aktif di luar jam kerja", "partly"];
  pill.textContent = text;
  pill.className = `bot-pill ${kind}`;
  pill.hidden = false;
  renderBotState();
}

function openBotSettings() {
  const bot = chat.bot;
  if (!bot) return;
  for (const radio of document.querySelectorAll('input[name="bot-mode"]')) radio.checked = radio.value === bot.mode;
  $("bot-store-info").value = bot.store_info;
  $("bot-telegram").checked = bot.telegram;
  $("bot-quiet").checked = bot.quiet_outside_hours;
  $("bot-not-configured").hidden = bot.configured;
  $("bot-telegram-missing").hidden = bot.telegram_configured;
  $("bot-month").textContent = `Bulan ini: bot menjawab ${bot.month_replies} pesan, ${bot.month_handoffs} chat diteruskan ke admin.`;
  $("bot-settings-status").textContent = "";
  $("chat-bot-settings").hidden = false;
  $("chat-layout").hidden = true;
  $("chat-bot-settings-open").setAttribute("aria-expanded", "true");
  document.querySelector('input[name="bot-mode"]:checked')?.focus();
}

function closeBotSettings() {
  $("chat-bot-settings").hidden = true;
  $("chat-layout").hidden = false;
  $("chat-bot-settings-open").setAttribute("aria-expanded", "false");
}

$("chat-bot-settings-open").addEventListener("click", () => {
  if ($("chat-bot-settings").hidden) openBotSettings();
  else closeBotSettings();
});
$("chat-bot-settings-close").addEventListener("click", closeBotSettings);
$("chat-bot-toggle").addEventListener("click", toggleBot);
$("chat-bot-settings").addEventListener("submit", async (event) => {
  event.preventDefault();
  const mode = document.querySelector('input[name="bot-mode"]:checked');
  const status = $("bot-settings-status");
  status.textContent = "Menyimpan…";
  try {
    await send("PUT", "/chat/bot-settings", {
      mode: mode ? mode.value : "always",
      store_info: $("bot-store-info").value,
      telegram: $("bot-telegram").checked,
      quiet_outside_hours: $("bot-quiet").checked,
    });
    status.textContent = "Disimpan.";
    await loadBotSettings();
  } catch (error) {
    if (!handleAuth(error)) {
      status.textContent = error.status === 400 ? "Info toko maksimal 4.000 huruf." : "Belum tersimpan. Coba lagi.";
    }
  }
});
