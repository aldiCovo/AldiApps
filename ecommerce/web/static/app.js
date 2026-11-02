"use strict";

const pageSize = 24;
const $ = (id) => document.getElementById(id);
const rupiah = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });

const state = { q: "", categoryId: "", size: "", color: "", inStock: false, offset: 0, total: 0, categories: new Map() };
let latestRequest = 0;

class ApiError extends Error {
  constructor(status) {
    super(`request failed with status ${status}`);
    this.status = status;
  }
}

async function api(path, options = {}) {
  const response = await fetch(`/api${path}`, options);
  if (!response.ok) throw new ApiError(response.status);
  return response.status === 204 ? null : response.json();
}

function post(path, body) {
  return api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
}

// Everything from the API goes through textContent, so its text can't become HTML.
function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function setStatus(text, isError = false) {
  $("status").textContent = text;
  $("status").classList.toggle("error", isError);
}

function totalStock(product) {
  return (product.variants || []).reduce((sum, variant) => sum + variant.stock, 0);
}

function fillSwatch(node, product) {
  node.className = `swatch tone-${product.id % 5}`;
  node.textContent = Array.from(product.name)[0] || "";
}

function productCard(product) {
  const card = el("button", "card");
  card.type = "button";
  const swatch = el("span");
  swatch.setAttribute("aria-hidden", "true");
  fillSwatch(swatch, product);
  card.append(swatch, el("span", "card-name", product.name));
  const category = state.categories.get(product.category_id);
  if (category) card.append(el("span", "card-meta", category));
  card.append(el("span", "card-price", rupiah.format(product.price)));
  if (totalStock(product) === 0) card.append(el("span", "card-meta sold-out", "Stok habis"));
  card.addEventListener("click", () => openProduct(product.id));
  return card;
}

function searchParams() {
  const params = new URLSearchParams({ limit: pageSize, offset: state.offset });
  if (state.q) params.set("q", state.q);
  if (state.categoryId) params.set("category_id", state.categoryId);
  if (state.size) params.set("size", state.size);
  if (state.color) params.set("color", state.color);
  if (state.inStock) params.set("in_stock", "true");
  return params;
}

async function loadProducts(append = false) {
  const request = ++latestRequest;
  state.offset = append ? state.offset + pageSize : 0;
  if (!append) setStatus("Memuat produk…");
  try {
    const result = await api(`/search?${searchParams()}`);
    if (request !== latestRequest) return;
    state.total = result.total;
    if (!append) $("products").replaceChildren();
    $("products").append(...result.products.map(productCard));
    setStatus(result.total === 0 ? "Produk tidak ditemukan. Coba kata atau saringan lain." : `${result.total} produk`);
    $("more").hidden = state.offset + pageSize >= result.total || state.offset + pageSize > 9900;
  } catch {
    if (request !== latestRequest) return;
    if (append) state.offset -= pageSize;
    if (!append) $("products").replaceChildren();
    setStatus("Produk belum bisa dimuat. Coba lagi sebentar lagi.", true);
  }
}

async function loadCategories() {
  try {
    const { categories } = await api("/categories");
    state.categories = new Map(categories.map((category) => [category.id, category.name]));
  } catch {
    state.categories = new Map();
  }
  renderCategories();
}

function renderCategories() {
  const chip = (label, id) => {
    const button = el("button", "chip", label);
    button.type = "button";
    button.setAttribute("aria-pressed", String(state.categoryId === id));
    button.addEventListener("click", () => {
      state.categoryId = id;
      renderCategories();
      loadProducts();
    });
    return button;
  };
  const chips = [chip("Semua", "")];
  for (const [id, name] of state.categories) chips.push(chip(name, String(id)));
  $("categories").replaceChildren(...chips);
}

function addOptions(select, values) {
  for (const value of values) {
    const option = el("option", "", value);
    option.value = value;
    select.append(option);
  }
}

// The filter choices come from the products themselves, up to the API's page limit.
async function loadFilterChoices() {
  try {
    const { products } = await api("/search?limit=100");
    const variants = products.flatMap((product) => product.variants || []);
    const unique = (key) => [...new Set(variants.map((variant) => variant[key]))].sort((a, b) => a.localeCompare(b, "id"));
    addOptions($("size-filter"), unique("size"));
    addOptions($("color-filter"), unique("color"));
  } catch {
    // The list above already shows an error, and the page works without these choices.
  }
}

function variantChoice(variant) {
  const soldOut = variant.stock === 0;
  const row = el("label", soldOut ? "variant unavailable" : "variant");
  const input = el("input");
  input.type = "radio";
  input.name = "variant";
  input.value = variant.id;
  input.disabled = soldOut;
  input.dataset.stock = variant.stock;
  row.append(input, el("span", "", `${variant.size} · ${variant.color}`),
    el("span", soldOut ? "variant-stock sold-out" : "variant-stock", soldOut ? "Stok habis" : `Stok ${variant.stock}`));
  return row;
}

let shownProductId = null;

async function openProduct(id) {
  try {
    const product = await api(`/products/${encodeURIComponent(id)}`);
    fillSwatch($("product-swatch"), product);
    $("product-title").textContent = product.name;
    $("product-price").textContent = rupiah.format(product.price);
    $("product-description").textContent = product.description;
    $("product-variants").replaceChildren(...(product.variants || []).map(variantChoice));
    $("order-quantity").value = 1;
    $("order-error").hidden = true;
    $("cart-note").hidden = true;
    shownProductId = product.id;
    const first = document.querySelector('input[name="variant"]:not(:disabled)');
    if (first) first.checked = true;
    $("cart-add").disabled = !first;
    $("order-now").disabled = !first;
    $("product-dialog").showModal();
  } catch (error) {
    setStatus(error.status === 404 ? "Produk ini sudah tidak tersedia." : "Produk belum bisa dibuka. Coba lagi sebentar lagi.", true);
  }
}

let authMode = "login";
let loggedIn = false;

function setAuthMode(mode) {
  authMode = mode;
  const registering = mode === "register";
  $("auth-title").textContent = registering ? "Daftar" : "Masuk";
  $("auth-submit").textContent = registering ? "Daftar" : "Masuk";
  $("auth-switch-text").textContent = registering ? "Sudah punya akun?" : "Belum punya akun?";
  $("auth-switch").textContent = registering ? "Masuk" : "Daftar";
  $("name-field").hidden = !registering;
  $("auth-name").required = registering;
  $("password-hint").hidden = !registering;
  $("auth-password").autocomplete = registering ? "new-password" : "current-password";
  $("auth-error").hidden = true;
}

function openAuth() {
  $("auth-form").reset();
  setAuthMode("login");
  $("auth-dialog").showModal();
}

function authMessage(error) {
  switch (error.status) {
    case 400: return "Periksa kembali data yang kamu isi.";
    case 401: return "Email atau password salah.";
    case 409: return "Email ini sudah terdaftar. Silakan masuk.";
    case 429: return "Terlalu banyak percobaan. Tunggu sebentar lalu coba lagi.";
    default: return "Layanan belum tersedia. Coba lagi sebentar lagi.";
  }
}

async function submitAuth(event) {
  event.preventDefault();
  const email = $("auth-email").value.trim();
  const password = $("auth-password").value;
  $("auth-submit").disabled = true;
  $("auth-error").hidden = true;
  try {
    if (authMode === "register") {
      await post("/register", { email, name: $("auth-name").value.trim(), password });
    }
    renderAccount(await post("/login", { email, password }));
    $("auth-form").reset();
    $("auth-dialog").close();
  } catch (error) {
    $("auth-error").textContent = authMessage(error);
    $("auth-error").hidden = false;
  } finally {
    $("auth-submit").disabled = false;
  }
}

function renderAccount(user) {
  loggedIn = Boolean(user);
  const account = $("account");
  if (!user) {
    const login = el("button", "secondary", "Masuk");
    login.type = "button";
    login.addEventListener("click", openAuth);
    account.replaceChildren(login);
    return;
  }
  const logout = el("button", "link", "Keluar");
  logout.type = "button";
  logout.addEventListener("click", async () => {
    try {
      await post("/logout", {});
      renderAccount(null);
    } catch {
      setStatus("Belum bisa keluar. Coba lagi sebentar lagi.", true);
    }
  });
  const orders = el("button", "link", "Pesanan");
  orders.type = "button";
  orders.addEventListener("click", openOrders);
  account.replaceChildren(el("span", "", `Halo, ${user.name}`), orders, logout);
}

async function loadAccount() {
  try {
    renderAccount(await api("/me"));
  } catch {
    renderAccount(null);
  }
}

const orderStates = {
  pending: ["Memeriksa stok…", ""],
  awaiting_payment: ["Menunggu pembayaran", ""],
  paying: ["Memproses pembayaran…", ""],
  paid: ["Dibayar", "good"],
  rejected: ["Ditolak: stok tidak cukup", "bad"],
};
const pollEvery = 1500;
// Signals may be lost, so a slow poll still runs while the connection is open.
const pollEveryWhileLive = 15000;
const liveRetryMax = 30000;
const unsettled = new Set(["pending", "paying"]);
const openStatuses = new Set(["pending", "awaiting_payment", "paying"]);
let pollTimer = null;
let shownOrders = [];
let liveSocket = null;
let liveTimer = null;
let liveRetry = 1000;

const isLive = () => liveSocket !== null && liveSocket.readyState === WebSocket.OPEN;

function orderState(order) {
  if (order.status === "cancelled") {
    return [order.reason === "expired" ? "Dibatalkan: waktu pembayaran habis" : "Dibatalkan: pembayaran gagal", "bad"];
  }
  return orderStates[order.status] || [order.status, ""];
}

function payButton(order, label, className, simulate) {
  const button = el("button", className, label);
  button.type = "button";
  button.addEventListener("click", async () => {
    for (const other of button.parentElement.querySelectorAll("button")) other.disabled = true;
    try {
      await post(`/orders/${order.id}/pay`, { simulate });
      $("orders-status").textContent = "";
    } catch (error) {
      $("orders-status").textContent = error.status === 409 ? "Pesanan ini sudah tidak bisa dibayar." : "Pembayaran belum bisa diproses. Coba lagi sebentar lagi.";
    }
    await refreshOrders();
  });
  return button;
}

function orderCard(order) {
  const card = el("article", "order");
  const head = el("div", "order-head");
  const [label, tone] = orderState(order);
  head.append(el("span", "", `Pesanan #${order.id}`), el("span", `order-state ${tone}`, label));
  card.append(head);
  for (const item of order.items) {
    card.append(el("p", "order-item", `${item.product_name} · ${item.size} · ${item.color} × ${item.quantity}`));
  }
  card.append(el("p", "card-price", `Total ${rupiah.format(order.total)}`));
  if (order.status === "rejected") {
    const again = el("button", "link", "Masukkan lagi ke keranjang");
    again.type = "button";
    again.addEventListener("click", () => {
      for (const item of order.items) addToCart(item.product_id, item.variant_id, item.quantity);
      $("orders-dialog").close();
      openCart();
    });
    card.append(again);
  }
  if (order.status === "awaiting_payment") {
    if (order.expires_at) {
      const time = new Date(order.expires_at).toLocaleTimeString("id-ID", { hour: "2-digit", minute: "2-digit" });
      card.append(el("p", "order-item", `Bayar sebelum pukul ${time}. Setelah itu stok dikembalikan.`));
    }
    const actions = el("div", "order-actions");
    actions.append(payButton(order, "Bayar (simulasi)", "primary", "success"), payButton(order, "Simulasikan gagal bayar", "secondary", "failure"));
    card.append(actions);
  }
  return card;
}

// Updates fetch one order at a time, because the order list has a rate limit and these don't.
async function refreshOrders() {
  clearTimeout(pollTimer);
  try {
    const { orders } = await api("/orders?limit=20");
    renderOrders(orders);
  } catch (error) {
    if (error.status === 401) {
      renderAccount(null);
      $("orders-dialog").close();
      openAuth();
      return;
    }
    $("orders-status").textContent = error.status === 429 ? "Terlalu banyak permintaan. Tunggu sebentar." : "Pesanan belum bisa dimuat. Coba lagi sebentar lagi.";
  }
}

function renderOrders(orders) {
  shownOrders = orders;
  clearTimeout(pollTimer);
  $("orders-list").replaceChildren(...orders.map(orderCard));
  if (orders.length === 0) $("orders-status").textContent = "Belum ada pesanan.";
  if (!$("orders-dialog").open) return;
  if (orders.some((order) => openStatuses.has(order.status))) connectLive();
  const waiting = orders.filter((order) => unsettled.has(order.status));
  if (waiting.length > 0) pollTimer = setTimeout(() => updateOrders(waiting.map((order) => order.id)), isLive() ? pollEveryWhileLive : pollEvery);
}

// Replaces the given orders with their current state. An answer that arrives late can't
// overwrite a newer one, and an order from another tab is added to the top.
async function updateOrders(ids) {
  try {
    const updated = await Promise.all(ids.map((id) => api(`/orders/${id}`)));
    if (!$("orders-dialog").open) return;
    let orders = shownOrders;
    for (const order of updated) {
      const shown = orders.find((item) => item.id === order.id);
      if (!shown) orders = [order, ...orders];
      else if (order.updated_at >= shown.updated_at) orders = orders.map((item) => (item.id === order.id ? order : item));
    }
    renderOrders(orders);
  } catch {
    if ($("orders-dialog").open) renderOrders(shownOrders);
  }
}

function connectLive() {
  if (liveSocket !== null) return;
  clearTimeout(liveTimer);
  const scheme = location.protocol === "https:" ? "wss" : "ws";
  const socket = new WebSocket(`${scheme}://${location.host}/api/events`);
  liveSocket = socket;
  socket.addEventListener("open", () => {
    liveRetry = 1000;
    // Anything that changed before the connection opened sent its signal to nobody.
    const missed = shownOrders.filter((order) => openStatuses.has(order.status)).map((order) => order.id);
    if (missed.length > 0) updateOrders(missed);
  });
  socket.addEventListener("message", (event) => {
    try {
      updateOrders([JSON.parse(event.data).order_id]);
    } catch {
      // The next poll or signal still brings the status.
    }
  });
  socket.addEventListener("close", () => {
    if (liveSocket !== socket) return;
    liveSocket = null;
    if (!$("orders-dialog").open) return;
    liveTimer = setTimeout(connectLive, liveRetry);
    liveRetry = Math.min(liveRetry * 2, liveRetryMax);
    renderOrders(shownOrders);
  });
}

function disconnectLive() {
  clearTimeout(liveTimer);
  const socket = liveSocket;
  liveSocket = null;
  if (socket !== null) socket.close();
}

function openOrders() {
  $("orders-status").textContent = "";
  $("orders-dialog").showModal();
  refreshOrders();
}

function orderError(error) {
  const messages = {
    400: "Ada barang yang sudah tidak tersedia. Periksa lagi pilihanmu.",
    401: "Silakan masuk lagi.",
    409: "Masih ada 5 pesanan yang belum selesai. Selesaikan dulu pesanan itu.",
    429: "Terlalu banyak percobaan. Tunggu sebentar lalu coba lagi.",
  };
  if (error.status === 401) renderAccount(null);
  return messages[error.status] || "Pesanan belum bisa dibuat. Coba lagi sebentar lagi.";
}

async function orderNow() {
  if (!$("order-form").reportValidity()) return;
  if (!loggedIn) {
    openAuth();
    return;
  }
  const chosen = document.querySelector('input[name="variant"]:checked');
  if (!chosen) return;
  $("order-now").disabled = true;
  $("order-error").hidden = true;
  try {
    await post("/orders", { items: [{ variant_id: Number(chosen.value), quantity: Number($("order-quantity").value) }] });
    $("product-dialog").close();
    openOrders();
  } catch (error) {
    $("order-error").textContent = orderError(error);
    $("order-error").hidden = false;
  } finally {
    $("order-now").disabled = false;
  }
}

// The cart lives in this browser and holds only ids and quantities. Names, prices and stock
// are read again each time it opens, so it never shows old ones.
const cartKey = "prastika-cart";
const cartMaxItems = 20;
const cartMaxQuantity = 20;
const isPositiveInt = (value) => Number.isSafeInteger(value) && value > 0;
let cart = loadCart();
let cartDetails = new Map();
let latestCartRequest = 0;

function loadCart() {
  try {
    const saved = JSON.parse(localStorage.getItem(cartKey));
    if (!Array.isArray(saved)) return [];
    const seen = new Set();
    return saved.filter((item) => {
      if (!item || !isPositiveInt(item.product_id) || !isPositiveInt(item.variant_id) || !isPositiveInt(item.quantity)) return false;
      if (item.quantity > cartMaxQuantity || seen.has(item.variant_id)) return false;
      seen.add(item.variant_id);
      return true;
    }).slice(0, cartMaxItems);
  } catch {
    return [];
  }
}

function saveCart() {
  try {
    localStorage.setItem(cartKey, JSON.stringify(cart));
  } catch {
    // Without storage the cart still works until the page closes.
  }
  $("cart-count").textContent = cart.reduce((sum, item) => sum + item.quantity, 0);
}

function addToCart(productId, variantId, quantity) {
  const item = cart.find((entry) => entry.variant_id === variantId);
  if (item) {
    item.quantity = Math.min(item.quantity + quantity, cartMaxQuantity);
  } else if (cart.length < cartMaxItems) {
    cart.push({ product_id: productId, variant_id: variantId, quantity: Math.min(quantity, cartMaxQuantity) });
  } else {
    return false;
  }
  saveCart();
  return true;
}

function submitCartAdd(event) {
  event.preventDefault();
  const chosen = document.querySelector('input[name="variant"]:checked');
  if (!chosen) return;
  const variantId = Number(chosen.value);
  const wanted = Number($("order-quantity").value);
  const inCart = cart.find((item) => item.variant_id === variantId)?.quantity || 0;
  const limit = Math.min(Number(chosen.dataset.stock), cartMaxQuantity);
  const room = limit - inCart;
  $("order-error").hidden = true;
  $("cart-note").hidden = true;
  if (room <= 0) {
    $("order-error").textContent = `Keranjang sudah berisi ${inCart} barang ini, jumlah terbanyak yang bisa dipesan.`;
    $("order-error").hidden = false;
    return;
  }
  if (!addToCart(shownProductId, variantId, Math.min(wanted, room))) {
    $("order-error").textContent = `Keranjang penuh. Satu pesanan paling banyak ${cartMaxItems} jenis barang.`;
    $("order-error").hidden = false;
    return;
  }
  $("cart-note-text").textContent = wanted > room ? `Hanya ${room} yang bisa ditambahkan, sesuai stok.` : "Masuk ke keranjang.";
  $("cart-note").hidden = false;
}

function cartRow(item) {
  const { product, variant } = cartDetails.get(item.variant_id);
  const soldOut = variant.stock === 0;
  const row = el("article", "cart-row");
  const about = el("div");
  about.append(el("div", "cart-name", product.name), el("div", "cart-detail", `${variant.size} · ${variant.color} · ${rupiah.format(product.price)}`));
  const subtotal = el("div", "cart-subtotal", soldOut ? "" : rupiah.format(product.price * item.quantity));
  const actions = el("div", "cart-actions");
  if (soldOut) {
    actions.append(el("span", "sold-out", "Stok habis"));
  } else {
    const label = el("label", "", "Jumlah");
    const input = el("input");
    input.type = "number";
    input.min = 1;
    input.max = Math.min(variant.stock, cartMaxQuantity);
    input.value = item.quantity;
    input.addEventListener("change", () => {
      const wanted = Math.trunc(Number(input.value)) || 1;
      item.quantity = Math.min(Math.max(wanted, 1), Number(input.max));
      input.value = item.quantity;
      subtotal.textContent = rupiah.format(product.price * item.quantity);
      saveCart();
      updateCartTotal();
    });
    label.append(input);
    actions.append(label);
  }
  const remove = el("button", "link", "Hapus");
  remove.type = "button";
  remove.setAttribute("aria-label", `Hapus ${product.name} ${variant.size} ${variant.color}`);
  remove.addEventListener("click", () => {
    cart = cart.filter((entry) => entry !== item);
    saveCart();
    renderCart();
    $("cart-title").focus();
  });
  actions.append(remove);
  row.append(about, subtotal, actions);
  return row;
}

function updateCartTotal() {
  const lines = cart.map((item) => ({ item, ...cartDetails.get(item.variant_id) }));
  const total = lines.reduce((sum, { item, product, variant }) => sum + (variant.stock === 0 ? 0 : product.price * item.quantity), 0);
  $("cart-total").textContent = rupiah.format(total);
  $("cart-checkout").disabled = lines.some(({ variant }) => variant.stock === 0);
}

function renderCart(note = "") {
  $("cart-list").replaceChildren(...cart.map(cartRow));
  $("cart-footer").hidden = cart.length === 0;
  if (cart.length === 0) {
    $("cart-status").textContent = note ? `${note} Keranjangmu kosong.` : "Keranjangmu masih kosong.";
    return;
  }
  const soldOut = cart.some((item) => cartDetails.get(item.variant_id).variant.stock === 0);
  $("cart-status").textContent = [note, soldOut ? "Hapus barang yang stoknya habis untuk memesan." : ""].filter(Boolean).join(" ");
  updateCartTotal();
}

async function refreshCart() {
  const request = ++latestCartRequest;
  $("cart-error").hidden = true;
  $("cart-footer").hidden = true;
  $("cart-list").replaceChildren();
  $("cart-status").textContent = cart.length > 0 ? "Memuat keranjang…" : "";
  const ids = [...new Set(cart.map((item) => item.product_id))];
  const results = await Promise.allSettled(ids.map((id) => api(`/products/${id}`)));
  if (request !== latestCartRequest) return;
  if (results.some((result) => result.status === "rejected" && result.reason.status !== 404)) {
    $("cart-status").textContent = "Keranjang belum bisa dimuat. Tutup lalu buka lagi sebentar lagi.";
    return;
  }
  cartDetails = new Map();
  results.forEach((result) => {
    if (result.status !== "fulfilled") return;
    for (const variant of result.value.variants || []) cartDetails.set(variant.id, { product: result.value, variant });
  });
  const before = cart.length;
  cart = cart.filter((item) => cartDetails.has(item.variant_id));
  const removed = before - cart.length;
  let reduced = 0;
  for (const item of cart) {
    const { stock } = cartDetails.get(item.variant_id).variant;
    if (stock > 0 && item.quantity > stock) {
      item.quantity = stock;
      reduced++;
    }
  }
  saveCart();
  const notes = [];
  if (removed > 0) notes.push(`${removed} barang dihapus karena sudah tidak dijual.`);
  if (reduced > 0) notes.push(`${reduced} barang dikurangi jumlahnya, sesuai stok.`);
  renderCart(notes.join(" "));
}

function openCart() {
  $("cart-dialog").showModal();
  refreshCart();
}

async function checkout() {
  if (!loggedIn) {
    openAuth();
    return;
  }
  $("cart-checkout").disabled = true;
  $("cart-error").hidden = true;
  try {
    await post("/orders", { items: cart.map(({ variant_id, quantity }) => ({ variant_id, quantity })) });
    cart = [];
    saveCart();
    $("cart-dialog").close();
    openOrders();
  } catch (error) {
    $("cart-error").textContent = orderError(error);
    $("cart-error").hidden = false;
    $("cart-checkout").disabled = false;
  }
}

$("order-form").addEventListener("submit", submitCartAdd);
$("order-now").addEventListener("click", orderNow);
$("cart-open").addEventListener("click", openCart);
$("cart-note-open").addEventListener("click", () => {
  $("product-dialog").close();
  openCart();
});
$("cart-checkout").addEventListener("click", checkout);
// Keeps a second tab showing the same cart.
window.addEventListener("storage", (event) => {
  if (event.key !== cartKey && event.key !== null) return;
  cart = loadCart();
  saveCart();
  if ($("cart-dialog").open) refreshCart();
});
$("orders-dialog").addEventListener("close", () => {
  clearTimeout(pollTimer);
  disconnectLive();
});
$("search-form").addEventListener("submit", (event) => {
  event.preventDefault();
  state.q = $("search-input").value.trim();
  loadProducts();
});
$("size-filter").addEventListener("change", (event) => { state.size = event.target.value; loadProducts(); });
$("color-filter").addEventListener("change", (event) => { state.color = event.target.value; loadProducts(); });
$("stock-filter").addEventListener("change", (event) => { state.inStock = event.target.checked; loadProducts(); });
$("more").addEventListener("click", () => loadProducts(true));
$("auth-form").addEventListener("submit", submitAuth);
$("auth-switch").addEventListener("click", () => setAuthMode(authMode === "login" ? "register" : "login"));
for (const button of document.querySelectorAll("[data-close]")) {
  button.addEventListener("click", () => button.closest("dialog").close());
}

saveCart();
renderCategories();
loadAccount();
loadFilterChoices();
// Category names appear on the cards, so the list loads after them.
loadCategories().then(() => loadProducts());
