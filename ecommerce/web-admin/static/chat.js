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
};
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
  badge.setAttribute("aria-label", `${waiting} belum dibalas`);
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
  $("chat-tab-waiting").setAttribute("aria-pressed", String(chat.filter === "waiting"));
  $("chat-tab-all").setAttribute("aria-pressed", String(chat.filter === "all"));
  refreshChat(true);
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
  $("chat-tab-waiting").textContent = `Belum dibalas (${page.waiting})`;
  renderChatList();
}

function renderChatList() {
  $("chat-list").replaceChildren(...chat.list.map(chatRow));
  $("chat-more").hidden = chat.list.length >= chat.total;
  $("chat-list-status").textContent = chat.list.length > 0 ? ""
    : chat.filter === "waiting" ? "Semua chat sudah dibalas." : "Belum ada pelanggan yang chat.";
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
  const text = (lastMessage.from === "admin" ? "Anda: " : "") + said;
  last.append(el("span", "chat-row-text", text));
  if (conversation.unread > 0) last.append(el("span", "chat-count", String(conversation.unread)));
  row.append(head, last);
  row.addEventListener("click", () => openConversation(conversation));
  item.append(row);
  return item;
}

async function openConversation(conversation) {
  chat.userId = conversation.user_id;
  chat.messages = [];
  chat.hasMore = false;
  chat.readUpTo = 0;
  $("chat-view").classList.add("thread-open");
  $("chat-empty").hidden = true;
  $("chat-open").hidden = false;
  $("chat-who").textContent = customerName(conversation);
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
  const lastMine = chat.messages.findLast((message) => message.from === "admin");
  for (const message of chat.messages) {
    const day = new Date(message.created_at).toDateString();
    if (day !== lastDay) {
      lines.push(el("p", "chat-day", chatDay.format(new Date(message.created_at))));
      lastDay = day;
    }
    const line = el("div", `chat-line ${message.from === "admin" ? "mine" : "theirs"}`);
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
$("chat-pick-search").addEventListener("input", () => { if (chat.catalog) showChatPicks(); });
$("chat-pick-search").addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    closeChatPicker();
    $("chat-attach").focus();
  }
});
$("chat-back").addEventListener("click", closeConversation);
$("chat-more").addEventListener("click", () => loadChatList(false, chatPageSize, chat.list.length));
for (const [id, filter] of [["chat-tab-waiting", "waiting"], ["chat-tab-all", "all"]]) {
  $(id).addEventListener("click", () => {
    chat.filter = filter;
    chat.list = [];
    openChat();
  });
}
document.addEventListener("visibilitychange", () => {
  if (!document.hidden && !$("chat-view").hidden) refreshChat(false);
});
