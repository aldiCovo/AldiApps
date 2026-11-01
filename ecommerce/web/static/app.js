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
  row.append(input, el("span", "", `${variant.size} · ${variant.color}`),
    el("span", soldOut ? "variant-stock sold-out" : "variant-stock", soldOut ? "Stok habis" : `Stok ${variant.stock}`));
  return row;
}

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
    const first = document.querySelector('input[name="variant"]:not(:disabled)');
    if (first) first.checked = true;
    $("order-submit").disabled = !first;
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
const unsettled = new Set(["pending", "paying"]);
let pollTimer = null;

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

// Polls one order at a time, because the order list has a rate limit and these don't.
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
  $("orders-list").replaceChildren(...orders.map(orderCard));
  if (orders.length === 0) $("orders-status").textContent = "Belum ada pesanan.";
  const waiting = orders.filter((order) => unsettled.has(order.status));
  if (waiting.length > 0 && $("orders-dialog").open) pollTimer = setTimeout(() => pollOrders(orders, waiting), pollEvery);
}

async function pollOrders(orders, waiting) {
  try {
    const updated = await Promise.all(waiting.map((order) => api(`/orders/${order.id}`)));
    const byId = new Map(updated.map((order) => [order.id, order]));
    renderOrders(orders.map((order) => byId.get(order.id) || order));
  } catch {
    if ($("orders-dialog").open) pollTimer = setTimeout(() => pollOrders(orders, waiting), pollEvery * 2);
  }
}

function openOrders() {
  $("orders-status").textContent = "";
  $("orders-dialog").showModal();
  refreshOrders();
}

async function submitOrder(event) {
  event.preventDefault();
  if (!loggedIn) {
    openAuth();
    return;
  }
  const chosen = document.querySelector('input[name="variant"]:checked');
  if (!chosen) return;
  $("order-submit").disabled = true;
  $("order-error").hidden = true;
  try {
    await post("/orders", { items: [{ variant_id: Number(chosen.value), quantity: Number($("order-quantity").value) }] });
    $("product-dialog").close();
    openOrders();
  } catch (error) {
    const messages = {
      401: "Silakan masuk lagi.",
      409: "Masih ada 5 pesanan yang belum selesai. Selesaikan dulu pesanan itu.",
      429: "Terlalu banyak percobaan. Tunggu sebentar lalu coba lagi.",
    };
    if (error.status === 401) renderAccount(null);
    $("order-error").textContent = messages[error.status] || "Pesanan belum bisa dibuat. Coba lagi sebentar lagi.";
    $("order-error").hidden = false;
  } finally {
    $("order-submit").disabled = false;
  }
}

$("order-form").addEventListener("submit", submitOrder);
$("orders-dialog").addEventListener("close", () => clearTimeout(pollTimer));
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

renderCategories();
loadAccount();
loadFilterChoices();
// Category names appear on the cards, so the list loads after them.
loadCategories().then(() => loadProducts());
