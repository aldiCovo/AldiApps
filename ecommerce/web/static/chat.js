"use strict";

// Chat with the shop's admins. It runs after app.js and account.js and shares their global
// scope. While the panel is open, a WebSocket says when the shop answers; while it is closed,
// the button asks now and then how many answers wait.

const chatHours = { from: 8, to: 20, days: [1, 2, 3, 4, 5, 6] }; // Monday–Saturday, WIB
const chatPollOpen = 15000;
const chatPollClosed = 60000;
const chatTime = new Intl.DateTimeFormat("id-ID", { hour: "2-digit", minute: "2-digit" });
const chatDayName = new Intl.DateTimeFormat("id-ID", { weekday: "long", day: "numeric", month: "long" });

const chatState = {
  user: null,
  messages: [],
  hasMore: false,
  readUpTo: 0,
  loaded: false,
  sending: false,
  openAfterLogin: false,
  socket: null,
  retry: 1000,
  retryTimer: 0,
  pollTimer: 0,
  unreadTimer: 0,
  // The card that goes with the next message, and the products seen lately, newest first.
  pending: null,
  recentProducts: [],
  // Who answers now: the bot, or a person the chat waits for, whether the bot is writing, and
  // whether the menu buttons answer.
  status: { botOn: false, needsAdmin: false, working: false, menuOn: false },
  // A menu button is being answered.
  menuBusy: false,
  // The menu's chips show every button, rather than the ones that follow the last answer.
  menuAll: false,
};
const chatRecentMax = 5;

try {
  const saved = JSON.parse(sessionStorage.getItem("chat-recent") || "[]");
  if (Array.isArray(saved)) chatState.recentProducts = saved.slice(0, chatRecentMax);
} catch {
  // Without storage the list starts empty.
}

// The status words on order cards: what the order was when the card was sent.
const chatOrderStatus = {
  pending: "Diproses", awaiting_payment: "Menunggu pembayaran", paying: "Memproses pembayaran", paid: "Dibayar",
  packed: "Dikemas", shipped: "Dikirim", arrived: "Sudah sampai", completed: "Selesai", rejected: "Ditolak", cancelled: "Dibatalkan",
};

function chatIcon() {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", "M4 5h16v11H9l-5 4z");
  svg.append(path);
  return svg;
}

// Called by openProduct in app.js, so the picker can offer what the customer just looked at.
function chatSawProduct(product) {
  const card = productAttachment(product);
  chatState.recentProducts = [card, ...chatState.recentProducts.filter((seen) => seen.id !== product.id)].slice(0, chatRecentMax);
  try {
    sessionStorage.setItem("chat-recent", JSON.stringify(chatState.recentProducts));
  } catch {
    // The list just isn't kept across reloads.
  }
}

function productAttachment(product) {
  return { kind: "product", id: product.id, name: product.name, price: product.price, category_id: product.category_id };
}

function orderAttachment(order) {
  const items = order.items || [];
  return {
    kind: "order", id: order.id, status: order.status, total: order.total,
    name: items.length > 0 ? items[0].product_name : "", more: Math.max(items.length - 1, 0),
  };
}

// Opens the chat with a product or order ready to send. The sheets it is asked from are modal,
// so they close first or they would cover the chat.
function askAbout(card) {
  for (const id of ["product-dialog", "orders-dialog"]) if ($(id).open) $(id).close();
  setChatPending(card);
  openChat();
}

// A product or order card, in a message, in the picker, or waiting to be sent.
function chatCard(card, onClick) {
  const box = el(onClick ? "button" : "div", `chat-card ${card.kind}`);
  if (onClick) {
    box.type = "button";
    box.addEventListener("click", onClick);
  }
  const thumb = el("span", "chat-card-thumb");
  thumb.setAttribute("aria-hidden", "true");
  if (card.kind === "product") {
    const url = photosOf({ id: card.id, category_id: card.category_id })[0];
    if (url) {
      const image = el("img");
      image.src = url;
      image.alt = "";
      image.loading = "lazy";
      image.decoding = "async";
      image.addEventListener("error", () => image.remove());
      thumb.append(image);
    }
  } else {
    thumb.textContent = "#";
  }
  const text = el("span", "chat-card-text");
  if (card.kind === "product") {
    text.append(el("strong", "", card.name), el("span", "", rupiah.format(card.price)));
  } else {
    text.append(el("strong", "", `Pesanan #${card.id}`),
      el("span", "", `${chatOrderStatus[card.status] || card.status} · ${rupiah.format(card.total)}`));
    if (card.name) text.append(el("span", "", card.more > 0 ? `${card.name} dan ${card.more} lainnya` : card.name));
  }
  box.append(thumb, text);
  return box;
}

// What a card in the conversation opens.
function openChatCard(card) {
  if (card.kind === "product") openProduct(card.id);
  else openOrderDetail(card.id);
}

function setChatPending(card) {
  chatState.pending = card;
  const box = $("chat-pending");
  if (!card) {
    box.hidden = true;
    box.replaceChildren();
    return;
  }
  const remove = el("button", "chat-pending-remove", "×");
  remove.type = "button";
  remove.setAttribute("aria-label", "Hapus lampiran");
  remove.addEventListener("click", () => {
    setChatPending(null);
    $("chat-text").focus();
  });
  box.replaceChildren(chatCard(card), remove);
  box.hidden = false;
  $("chat-note").hidden = true;
  closeChatPicker();
}

async function openChatPicker() {
  $("chat-picker").hidden = false;
  $("chat-attach").setAttribute("aria-expanded", "true");
  const products = chatState.recentProducts.map((card) => chatCard(card, () => {
    setChatPending(card);
    $("chat-text").focus();
  }));
  $("chat-pick-products").replaceChildren(...(products.length > 0 ? products
    : [el("p", "chat-pick-empty", "Buka sebuah produk, lalu tekan Tanya soal produk ini.")]));
  $("chat-pick-orders").replaceChildren(el("p", "chat-pick-empty", "Memuat…"));
  try {
    const { orders } = await api("/orders?limit=5");
    if ($("chat-picker").hidden) return;
    $("chat-pick-orders").replaceChildren(...(orders.length > 0 ? orders.map((order) => chatCard(orderAttachment(order), () => {
      setChatPending(orderAttachment(order));
      $("chat-text").focus();
    })) : [el("p", "chat-pick-empty", "Belum ada pesanan.")]));
  } catch {
    $("chat-pick-orders").replaceChildren(el("p", "chat-pick-empty", "Pesanan belum bisa dimuat."));
  }
}

function closeChatPicker() {
  $("chat-picker").hidden = true;
  $("chat-attach").setAttribute("aria-expanded", "false");
}

const chatOpen = () => !$("chat-panel").hidden;

// Whether the shop is answering now, by the clock in Jakarta.
function chatOnline(now = new Date()) {
  const wib = new Date(now.getTime() + 7 * 3600 * 1000);
  const hour = wib.getUTCHours();
  return chatHours.days.includes(wib.getUTCDay()) && hour >= chatHours.from && hour < chatHours.to;
}

function showChatHours() {
  const hours = $("chat-hours");
  hours.replaceChildren();
  const online = chatOnline();
  const { botOn, needsAdmin } = chatState.status;
  const dot = el("span", botOn || (online && !needsAdmin) ? "chat-dot on" : needsAdmin ? "chat-dot wait" : "chat-dot");
  dot.setAttribute("aria-hidden", "true");
  hours.append(dot, document.createTextNode(botOn ? "Dijawab langsung oleh asisten · admin siap membantu"
    : needsAdmin ? (online ? "Menunggu admin · biasanya beberapa menit" : "Menunggu admin · dibalas Senin–Sabtu, 08.00–20.00 WIB")
    : online ? "Online · biasanya membalas dalam beberapa menit"
    : "Di luar jam kerja · dibalas Senin–Sabtu, 08.00–20.00 WIB"));
}

// The menu: buttons the bot answers from the shop's data, without an AI. The labels are what
// the customer's message says once pressed.
const chatMenuButtons = [
  { action: "orders", label: "Status pesanan saya", chip: "Status pesanan", icon: "M3 7l9-4 9 4v10l-9 4-9-4zM3 7l9 4 9-4M12 11v10" },
  { action: "stock", label: "Cek stok & ukuran", chip: "Cek stok", icon: "M8 3l-5 4 3 3 2-1v12h8V9l2 1 3-3-5-4c0 2-1.5 3-4 3S8 5 8 3z" },
  { action: "shipping", label: "Ongkir ke kotaku", chip: "Ongkir", icon: "M2 7h11v9H2zM13 10h4l4 3v3h-8zM6 20a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM17 20a2 2 0 1 0 0-4 2 2 0 0 0 0 4z" },
  { action: "vouchers", label: "Voucher saya", chip: "Voucher", icon: "M3 8a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v2a2 2 0 0 0 0 4v2a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-2a2 2 0 0 0 0-4z" },
  { action: "returns", label: "Cara retur & tukar", chip: "Retur & tukar", icon: "M4 4h16v12H8l-4 4zM8 9h8M8 12h5" },
  { action: "admin", label: "Bicara dengan admin", chip: "Bicara dengan admin", icon: "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 21c0-4 4-6 8-6s8 2 8 6" },
];

function menuIcon(d) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", d);
  svg.append(path);
  return svg;
}

function menuChip(label, onClick) {
  const chip = el("button", "chat-chip", label);
  chip.type = "button";
  chip.disabled = chatState.menuBusy;
  chip.addEventListener("click", onClick);
  return chip;
}

// pressMenu starts a button: stock first asks which product, the rest go straight to the bot.
function pressMenu(action) {
  chatState.menuAll = false;
  if (action === "stock") openMenuPick("stock");
  else chatMenu({ action });
}

// renderChatMenu shows the buttons as a grid in an empty chat, and as chips after messages:
// the ones that follow the last answer, or all of them.
function renderChatMenu() {
  const menu = $("chat-menu");
  const { menuOn, working, needsAdmin } = chatState.status;
  menu.hidden = !menuOn || working;
  if (menu.hidden) {
    menu.replaceChildren();
    return;
  }
  if (chatState.messages.length === 0) {
    const grid = el("div", "chat-menu-grid");
    for (const button of chatMenuButtons) {
      const tile = el("button", "chat-menu-tile");
      tile.dataset.action = button.action;
      tile.type = "button";
      tile.disabled = chatState.menuBusy;
      tile.append(menuIcon(button.icon), el("span", "", button.label));
      tile.addEventListener("click", () => pressMenu(button.action));
      grid.append(tile);
    }
    menu.replaceChildren(grid, el("p", "chat-menu-note", "Menu dijawab langsung dari data toko. Pertanyaan lain? Ketik saja di bawah."));
    return;
  }
  const chips = [];
  const asked = chatState.messages.findLast((message) => message.from === "customer");
  const lastAnswered = asked && chatState.messages.at(-1).from === "bot" ? asked.body : "";
  if (!chatState.menuAll && lastAnswered === "Status pesanan saya") {
    chips.push(
      menuChip("Ubah alamat / batal", () => chatMenu({ action: "admin", reason: "change_order" })),
      menuChip("Belum sampai", () => chatMenu({ action: "admin", reason: "not_arrived" })),
    );
  } else if (!chatState.menuAll && lastAnswered.startsWith("Ongkir ke")) {
    chips.push(menuChip("Provinsi lain", () => openMenuPick("province")));
  }
  if (chips.length > 0) {
    chips.push(menuChip("Menu lain", () => {
      chatState.menuAll = true;
      renderChatMenu();
    }));
  } else {
    for (const button of chatMenuButtons) {
      if (button.action === "admin" && needsAdmin) continue;
      chips.push(menuChip(button.chip, () => pressMenu(button.action)));
    }
  }
  const row = el("div", "chat-chips");
  row.setAttribute("role", "group");
  row.setAttribute("aria-label", "Menu cepat");
  row.append(...chips);
  menu.replaceChildren(row);
}

async function chatMenu(body) {
  if (chatState.menuBusy || !chatState.user) return;
  chatState.menuBusy = true;
  $("chat-note").hidden = true;
  closeMenuPick();
  renderChatMenu();
  try {
    const result = await post("/chat/menu", body);
    if (result && result.need === "province") {
      openMenuPick("province");
      return;
    }
    await loadChat(true);
  } catch (error) {
    if (error.status === 429) showChatNote("Terlalu cepat. Tunggu sebentar, lalu coba lagi.");
    else if (error.status === 401) {
      showChatNote("Sesi kamu sudah berakhir. Silakan masuk lagi.");
      loadAccount();
    } else if (error.status === 409) {
      showChatNote("Menu sedang tidak aktif. Tulis pesan, admin yang membalas.");
      loadChat(false);
    } else if (error.status === 404) showChatNote("Produk itu sudah tidak dijual.");
    else showChatNote("Belum bisa dijawab. Coba lagi sebentar lagi.");
  } finally {
    chatState.menuBusy = false;
    renderChatMenu();
  }
}

// The menu's own picker: which product for stock, which province for shipping.
let menuPickKind = "";
let menuPickTimer = 0;
let menuPickSearch = 0;

function openMenuPick(kind) {
  menuPickKind = kind;
  closeChatPicker();
  const stock = kind === "stock";
  $("chat-menu-pick-title").textContent = stock ? "Cek stok produk yang mana?" : "Ongkir ke provinsi mana?";
  $("chat-menu-pick-label").textContent = stock ? "Cari nama produk" : "Cari provinsi";
  const search = $("chat-menu-pick-search");
  search.value = "";
  search.placeholder = stock ? "Cari nama produk…" : "Cari provinsi…";
  $("chat-menu-pick").hidden = false;
  fillMenuPick("");
  search.focus();
}

function closeMenuPick() {
  menuPickKind = "";
  clearTimeout(menuPickTimer);
  $("chat-menu-pick").hidden = true;
}

async function fillMenuPick(query) {
  const list = $("chat-menu-pick-list");
  const kind = menuPickKind;
  const q = query.trim().toLowerCase();
  if (kind === "province") {
    try {
      shippingTable = shippingTable || await api("/shipping");
    } catch {
      list.replaceChildren(el("p", "chat-pick-empty", "Daftar provinsi belum bisa dimuat."));
      return;
    }
    if (menuPickKind !== kind) return;
    const provinces = shippingTable.provinces.filter((p) => p.name.toLowerCase().includes(q));
    list.replaceChildren(...(provinces.length > 0 ? provinces.map((p) => {
      const button = el("button", "chat-pick-province", p.name);
      button.type = "button";
      button.addEventListener("click", () => chatMenu({ action: "shipping", province: p.name }));
      return button;
    }) : [el("p", "chat-pick-empty", "Provinsi tidak ditemukan.")]));
    return;
  }
  const pick = (card) => chatCard(card, () => chatMenu({ action: "stock", product_id: card.id }));
  if (q === "") {
    const recent = chatState.recentProducts.map(pick);
    list.replaceChildren(...(recent.length > 0 ? [el("p", "chat-pick-empty", "Yang baru kamu lihat:"), ...recent]
      : [el("p", "chat-pick-empty", "Ketik nama produknya di atas.")]));
    return;
  }
  const mine = ++menuPickSearch;
  try {
    const result = await api(`/search?limit=5&q=${encodeURIComponent(q)}`);
    if (mine !== menuPickSearch || menuPickKind !== kind) return;
    list.replaceChildren(...(result.products.length > 0 ? result.products.map((product) => pick(productAttachment(product)))
      : [el("p", "chat-pick-empty", "Produk tidak ditemukan.")]));
  } catch {
    if (mine === menuPickSearch) list.replaceChildren(el("p", "chat-pick-empty", "Pencarian belum bisa dipakai."));
  }
}

function setChatCount(unread) {
  const count = $("chat-fab-count");
  count.hidden = !(unread > 0) || chatOpen();
  count.textContent = unread > 9 ? "9+" : String(unread || "");
  $("chat-fab").setAttribute("aria-label", unread > 0 ? `Chat admin, ${unread} pesan baru` : "Chat admin");
  // The envelope in the header shows the same count.
  const badge = $("header-chat-count");
  badge.hidden = count.hidden;
  badge.textContent = count.textContent;
  $("header-chat").setAttribute("aria-label", $("chat-fab").getAttribute("aria-label"));
}

// Called by renderAccount in app.js whenever the session is read or changes.
function chatUserChanged(user) {
  const changed = (user ? user.id : null) !== (chatState.user ? chatState.user.id : null);
  chatState.user = user;
  if (changed) {
    chatState.messages = [];
    chatState.loaded = false;
    chatState.readUpTo = 0;
    $("chat-lines").replaceChildren();
    setChatCount(0);
  }
  clearInterval(chatState.unreadTimer);
  if (!user) {
    setChatPending(null);
    if (chatOpen()) closeChat();
    return;
  }
  checkChatUnread();
  chatState.unreadTimer = setInterval(checkChatUnread, chatPollClosed);
  if (chatState.openAfterLogin) {
    chatState.openAfterLogin = false;
    openChat();
  }
}

function checkChatUnread() {
  if (!chatState.user || chatOpen() || document.hidden) return;
  api("/chat/unread").then(({ unread }) => setChatCount(unread), () => {});
}

function openChat() {
  if (!chatState.user) {
    chatState.openAfterLogin = true;
    openAuth("Masuk dulu untuk chat dengan admin. Bisa juga lewat WhatsApp +62 812 8270 9876.");
    return;
  }
  $("chat-panel").hidden = false;
  $("chat-fab").setAttribute("aria-expanded", "true");
  document.body.classList.add("chat-is-open");
  setChatCount(0);
  showChatHours();
  $("chat-note").hidden = true;
  loadChat(true);
  connectChat();
  clearInterval(chatState.pollTimer);
  chatState.pollTimer = setInterval(() => { if (!document.hidden) loadChat(false); }, chatPollOpen);
  $("chat-text").focus();
}

function closeChat() {
  closeChatPicker();
  $("chat-panel").hidden = true;
  $("chat-fab").setAttribute("aria-expanded", "false");
  document.body.classList.remove("chat-is-open");
  clearInterval(chatState.pollTimer);
  disconnectChat();
}

function connectChat() {
  if (chatState.socket || !chatOpen() || !chatState.user) return;
  clearTimeout(chatState.retryTimer);
  const scheme = location.protocol === "https:" ? "wss" : "ws";
  const socket = new WebSocket(`${scheme}://${location.host}/api/events`);
  chatState.socket = socket;
  socket.addEventListener("open", () => {
    chatState.retry = 1000;
    // Anything sent before the connection opened reached nobody.
    loadChat(false);
  });
  socket.addEventListener("message", (event) => {
    try {
      if (JSON.parse(event.data).type === "chat") loadChat(false);
    } catch {
      // The next poll brings it anyway.
    }
  });
  socket.addEventListener("close", () => {
    if (chatState.socket !== socket) return;
    chatState.socket = null;
    if (!chatOpen()) return;
    chatState.retryTimer = setTimeout(connectChat, chatState.retry);
    chatState.retry = Math.min(chatState.retry * 2, 30000);
  });
}

function disconnectChat() {
  clearTimeout(chatState.retryTimer);
  const socket = chatState.socket;
  chatState.socket = null;
  if (socket) socket.close();
}

// Loads the newest messages, or older ones before those shown.
async function loadChat(scrollDown, older = false) {
  const userId = chatState.user && chatState.user.id;
  if (!userId) return;
  const before = older && chatState.messages.length > 0 ? `&before=${chatState.messages[0].id}` : "";
  let page;
  try {
    page = await api(`/chat?limit=50${before}`);
  } catch (error) {
    if (!chatState.user || chatState.user.id !== userId) return;
    if (error.status === 401) {
      showChatNote("Sesi kamu sudah berakhir. Silakan masuk lagi.");
      loadAccount();
    } else if (!chatState.loaded) {
      showChatNote("Chat belum bisa dimuat. Coba lagi sebentar lagi.");
    }
    return;
  }
  if (!chatState.user || chatState.user.id !== userId) return;
  const box = $("chat-box");
  const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
  const oldHeight = box.scrollHeight;
  const shownBefore = chatState.messages.map((message) => message.id).join();
  const readBefore = chatState.readUpTo;
  const statusBefore = JSON.stringify(chatState.status);
  chatState.status = { botOn: page.bot_on, needsAdmin: page.needs_admin, working: page.bot_working, menuOn: page.menu_on };
  const statusChanged = statusBefore !== JSON.stringify(chatState.status);
  if (statusChanged) showChatHours();
  if (older) {
    chatState.messages = [...page.messages, ...chatState.messages];
    chatState.hasMore = page.has_more;
  } else {
    const first = page.messages.length > 0 ? page.messages[0].id : Infinity;
    const kept = chatState.messages.filter((message) => message.id < first);
    chatState.messages = [...kept, ...page.messages];
    if (kept.length === 0) chatState.hasMore = page.has_more;
  }
  chatState.readUpTo = page.read_up_to;
  chatState.loaded = true;
  if (!older && !scrollDown && !statusChanged && shownBefore === chatState.messages.map((message) => message.id).join() && readBefore === chatState.readUpTo) return;
  renderChat();
  if (older) box.scrollTop = box.scrollHeight - oldHeight;
  else if (scrollDown || atBottom) box.scrollTop = box.scrollHeight;
  if (!older && page.unread > 0 && chatOpen()) post("/chat/read", {}).catch(() => {});
}

function renderChat() {
  $("chat-older").hidden = !chatState.hasMore;
  $("chat-welcome").hidden = chatState.messages.length > 0;
  const name = chatState.user && chatState.user.name ? firstName(chatState.user.name) : "";
  $("chat-welcome").textContent = chatState.status.menuOn
    ? `Halo${name ? ` Kak ${name}` : ""}! Saya Asisten Prastika. Pilih menu di bawah, atau ketik pertanyaan Kakak langsung.`
    : "Halo! Ada yang bisa kami bantu? Tanya soal ukuran, stok, pesanan, atau pembayaran.";
  const lines = [];
  let lastDay = "";
  const lastMine = chatState.messages.findLast((message) => message.from === "customer");
  for (const message of chatState.messages) {
    const date = new Date(message.created_at);
    if (date.toDateString() !== lastDay) {
      lastDay = date.toDateString();
      const today = new Date().toDateString() === lastDay;
      lines.push(el("p", "chat-day", today ? "Hari ini" : chatDayName.format(date)));
    }
    const mine = message.from === "customer";
    const line = el("div", mine ? "chat-msg mine" : "chat-msg theirs");
    if (message.from === "bot") {
      line.classList.add("bot");
      const from = el("span", "chat-from");
      from.append(el("span", "chat-bot-tag", "bot"), document.createTextNode("Asisten Prastika"));
      line.append(from);
    }
    if (message.attachment) {
      const card = message.attachment;
      line.classList.add("with-card");
      line.append(chatCard(card, () => openChatCard(card)));
    }
    if (message.body) line.append(el("p", "chat-msg-body", message.body));
    let meta = chatTime.format(date);
    if (message === lastMine) meta += message.id <= chatState.readUpTo ? " · Dibaca" : " · Terkirim";
    line.append(el("span", "chat-msg-meta", meta));
    lines.push(line);
  }
  const { needsAdmin, working } = chatState.status;
  if (working) lines.push(el("p", "chat-typing", "Asisten Prastika sedang mengetik…"));
  else if (needsAdmin && chatState.messages.length > 0) lines.push(el("p", "chat-handed", "Diteruskan ke admin · Senin–Sabtu 08.00–20.00 WIB"));
  $("chat-lines").replaceChildren(...lines);
  renderChatMenu();
}

function showChatNote(text) {
  $("chat-note").textContent = text;
  $("chat-note").hidden = false;
}

async function sendChat() {
  const text = $("chat-text").value.trim();
  const card = chatState.pending;
  if ((!text && !card) || chatState.sending || !chatState.user) return;
  chatState.sending = true;
  $("chat-send").disabled = true;
  $("chat-note").hidden = true;
  try {
    const { message } = await post("/chat/messages", card ? { body: text, attach: { kind: card.kind, id: card.id } } : { body: text });
    $("chat-text").value = "";
    if (chatState.pending === card) setChatPending(null);
    fitChatText();
    if (!chatState.messages.some((shown) => shown.id === message.id)) chatState.messages.push(message);
    // The bot answers in a moment; say so at once rather than after it starts.
    if (chatState.status.botOn) chatState.status = { ...chatState.status, working: true };
    renderChat();
    $("chat-box").scrollTop = $("chat-box").scrollHeight;
  } catch (error) {
    if (error.status === 429) showChatNote("Pesanmu terlalu cepat. Tunggu sebentar, lalu kirim lagi.");
    else if (error.status === 401) {
      showChatNote("Sesi kamu sudah berakhir. Silakan masuk lagi.");
      loadAccount();
    } else if (error.status === 422) {
      setChatPending(null);
      showChatNote(card && card.kind === "order" ? "Pesanan itu tidak bisa dilampirkan." : "Produk itu sudah tidak dijual, jadi tidak bisa dilampirkan.");
    } else if (error.status === 502) showChatNote("Lampiran belum bisa diperiksa. Coba lagi sebentar lagi.");
    else if (error.status === 400) showChatNote("Pesan maksimal 1.000 huruf.");
    else showChatNote("Pesan belum terkirim. Periksa koneksi, lalu coba lagi.");
  } finally {
    chatState.sending = false;
    $("chat-send").disabled = false;
    $("chat-text").focus();
  }
}

// The box grows with the message, up to a few lines.
function fitChatText() {
  const text = $("chat-text");
  text.style.height = "auto";
  text.style.height = `${Math.min(text.scrollHeight, 120)}px`;
}

$("chat-fab").addEventListener("click", () => (chatOpen() ? closeChat() : openChat()));
$("chat-close").addEventListener("click", () => {
  closeChat();
  $("chat-fab").focus();
});
$("chat-older").addEventListener("click", () => loadChat(false, true));
$("chat-form").addEventListener("submit", (event) => {
  event.preventDefault();
  sendChat();
});
$("chat-text").addEventListener("input", fitChatText);
$("chat-text").addEventListener("keydown", (event) => {
  // On a phone, Enter makes a new line and the button sends.
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing && !phoneLayout.matches) {
    event.preventDefault();
    sendChat();
  }
});
$("chat-panel").addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  if (!$("chat-picker").hidden) {
    closeChatPicker();
    $("chat-attach").focus();
    return;
  }
  if (!$("chat-menu-pick").hidden) {
    closeMenuPick();
    $("chat-text").focus();
    return;
  }
  closeChat();
  $("chat-fab").focus();
});
$("akun-help-chat").addEventListener("click", () => openChat());
$("header-chat").addEventListener("click", () => (chatOpen() ? closeChat() : openChat()));
$("chat-attach").addEventListener("click", () => {
  closeMenuPick();
  if ($("chat-picker").hidden) openChatPicker();
  else closeChatPicker();
});
$("chat-menu-pick-close").addEventListener("click", () => {
  closeMenuPick();
  $("chat-text").focus();
});
$("chat-menu-pick-search").addEventListener("input", (event) => {
  clearTimeout(menuPickTimer);
  menuPickTimer = setTimeout(() => fillMenuPick(event.target.value), menuPickKind === "stock" ? 250 : 0);
});
$("chat-picker-close").addEventListener("click", () => {
  closeChatPicker();
  $("chat-attach").focus();
});
$("product-ask").addEventListener("click", () => {
  const product = chatState.recentProducts.find((seen) => seen.id === shownProductId);
  if (product) askAbout(product);
});
if ($("footer-chat")) $("footer-chat").addEventListener("click", () => openChat());
document.addEventListener("visibilitychange", () => {
  if (document.hidden) return;
  if (chatOpen()) loadChat(false);
  else checkChatUnread();
});
// Logging in from the auth dialog without opening the chat drops the wish to open it.
$("auth-dialog").addEventListener("close", () => {
  setTimeout(() => { if (!chatState.user) chatState.openAfterLogin = false; }, 2000);
});

// Should /me have answered before this file ran, catch up now.
if (accountKnown) chatUserChanged(currentUser);
