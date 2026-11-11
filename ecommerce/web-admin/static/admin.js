"use strict";

const $ = (id) => document.getElementById(id);
const rupiah = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });
const dateTime = new Intl.DateTimeFormat("id-ID", { dateStyle: "medium", timeStyle: "short" });
const when = (iso) => dateTime.format(new Date(iso));
const pageSize = 20;

const statusNames = {
  pending: ["Memeriksa stok", "wait"],
  awaiting_payment: ["Menunggu pembayaran", "wait"],
  paying: ["Memproses pembayaran", "wait"],
  paid: ["Dibayar · perlu dikemas", "todo"],
  packed: ["Dikemas", "todo"],
  shipped: ["Dikirim", "good"],
  arrived: ["Sampai · menunggu pembeli", "good"],
  completed: ["Selesai", "good"],
  rejected: ["Ditolak: stok habis", "bad"],
  cancelled: ["Dibatalkan", "bad"],
};
const tabNames = { paid: "Perlu dikemas", packed: "Dikemas", shipped: "Dikirim", arrived: "Sampai", completed: "Selesai" };

let filter = "paid";
let currentAdmin = null;
let shown = [];
let loadToken = 0;

class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function api(path, options = {}) {
  const response = await fetch(`/admin/api${path}`, options);
  if (!response.ok) {
    let message = "";
    try {
      message = (await response.json()).error || "";
    } catch {
      // Not JSON, such as a proxy's error page.
    }
    throw new ApiError(response.status, message);
  }
  return response.status === 204 ? null : response.json();
}

// Every change is sent as JSON, which a form on another site can't send.
function send(method, path, body) {
  const options = { method, headers: { "Content-Type": "application/json" } };
  if (body !== undefined) options.body = JSON.stringify(body);
  return api(path, options);
}

function post(path, body) {
  return send("POST", path, body);
}

// Everything from the API goes through textContent, so its text can't become HTML.
function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function button(label, className, onClick) {
  const node = el("button", className, label);
  node.type = "button";
  node.addEventListener("click", onClick);
  return node;
}

function show(view) {
  $("loading").hidden = true;
  $("login-view").hidden = view !== "login";
  $("orders-view").hidden = view !== "orders";
  $("products-view").hidden = view !== "products";
  $("users-view").hidden = view !== "users";
  $("vouchers-view").hidden = view !== "vouchers";
  $("looks-view").hidden = view !== "looks";
  $("who").hidden = view === "login";
  $("nav").hidden = view === "login";
  for (const link of document.querySelectorAll(".nav-link")) {
    if (link.dataset.view === view) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
}

// The page to open: #produk opens the products, #pengguna the users, anything else the orders.
function openView(view) {
  if (view === "products") {
    show("products");
    loadProducts();
  } else if (view === "users") {
    show("users");
    loadUsers(true);
  } else if (view === "vouchers") {
    show("vouchers");
    loadVouchers();
  } else if (view === "looks") {
    show("looks");
    loadLooks();
  } else {
    show("orders");
    loadOrders(true);
  }
}

for (const link of document.querySelectorAll(".nav-link")) {
  link.addEventListener("click", (event) => {
    event.preventDefault();
    history.replaceState(null, "", link.getAttribute("href"));
    openView(link.dataset.view);
  });
}

function showLogin(message) {
  show("login");
  $("login-error").hidden = !message;
  $("login-error").textContent = message || "";
  $("login-email").focus();
}

// A refused session means logging in again, wherever it happens.
function handleAuth(error) {
  if (error.status === 401 || error.status === 403) {
    showLogin(error.status === 403 ? "Akun ini bukan admin." : "Silakan masuk lagi.");
    return true;
  }
  return false;
}

async function start() {
  try {
    const me = await api("/me");
    loggedIn(me);
  } catch (error) {
    if (!handleAuth(error)) showLogin("Admin belum bisa dimuat. Coba lagi sebentar lagi.");
    else $("login-error").hidden = true;
  }
}

function loggedIn(me) {
  currentAdmin = me;
  $("who-name").textContent = `${me.name} (${me.email})`;
  const views = { "#produk": "products", "#pengguna": "users", "#voucher": "vouchers", "#look": "looks" };
  openView(views[location.hash] || "orders");
}

$("login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const email = $("login-email").value.trim();
  const password = $("login-password").value;
  if (!email || !password) {
    showLogin("Isi email dan kata sandi.");
    return;
  }
  $("login-submit").disabled = true;
  $("login-error").hidden = true;
  try {
    const me = await post("/login", { email, password });
    $("login-password").value = "";
    loggedIn(me);
  } catch (error) {
    const messages = {
      401: "Email atau kata sandi salah.",
      403: "Akun ini bukan admin.",
      429: "Terlalu banyak percobaan. Tunggu sebentar lalu coba lagi.",
    };
    showLogin(messages[error.status] || "Belum bisa masuk. Coba lagi sebentar lagi.");
  } finally {
    $("login-submit").disabled = false;
  }
});

$("logout").addEventListener("click", async () => {
  try {
    await post("/logout", {});
  } finally {
    shown = [];
    currentAdmin = null;
    $("orders-list").replaceChildren();
    $("user-list").replaceChildren();
    showLogin("");
  }
});

for (const tab of document.querySelectorAll("#orders-view .tab")) {
  tab.addEventListener("click", () => {
    filter = tab.dataset.status;
    for (const other of document.querySelectorAll("#orders-view .tab")) other.setAttribute("aria-pressed", String(other === tab));
    loadOrders(true);
  });
}
$("reload").addEventListener("click", () => loadOrders(true));
$("more").addEventListener("click", () => loadOrders(false));

// note stays in the status line after loading, such as why the list was reloaded.
async function loadOrders(reset, note = "") {
  const token = ++loadToken;
  if (reset) {
    shown = [];
    $("orders-list").replaceChildren();
  }
  $("orders-status").textContent = "Memuat pesanan…";
  $("more").hidden = true;
  const query = new URLSearchParams({ limit: String(pageSize), offset: String(shown.length) });
  if (filter) query.set("status", filter);
  try {
    const { orders } = await api(`/orders?${query}`);
    // A newer request, such as another tab clicked meanwhile, wins.
    if (token !== loadToken) return;
    shown = shown.concat(orders);
    $("orders-list").append(...orders.map(orderCard));
    $("more").hidden = orders.length < pageSize;
    $("orders-status").textContent = [note, shown.length === 0 ? "Tidak ada pesanan di sini." : ""].filter(Boolean).join(" ");
  } catch (error) {
    if (token !== loadToken || handleAuth(error)) return;
    $("orders-status").textContent = "Pesanan belum bisa dimuat. Coba lagi sebentar lagi.";
  }
}

function fact(list, name, iso, note = "") {
  if (iso) list.append(el("dt", "", name), el("dd", "", when(iso) + note));
}

const arrivedBy = { admin: " · ditandai admin", courier: " · dari kurir" };
const completedBy = { admin: " · ditandai admin", customer: " · dikonfirmasi pembeli", system: " · otomatis oleh sistem" };

function orderCard(order) {
  const card = el("article", "order");
  card.dataset.id = order.id;
  const head = el("div", "order-head");
  const [label, tone] = order.status === "rejected" && order.reason === "sale_sold_out" ? ["Ditolak: kuota Flash Sale habis", "bad"]
    : statusNames[order.status] || [order.status, ""];
  head.append(el("h2", "", `Pesanan #${order.id}`), el("span", `state ${tone}`, label));
  card.append(head);

  const customer = order.customer;
  card.append(el("p", "customer", customer ? `${customer.name} · ${customer.email}` : `Akun #${order.user_id} sudah dihapus`));

  const body = el("div", "order-body");
  const items = el("div", "items");
  for (const item of order.items) {
    const row = el("div", "item");
    row.append(
      el("span", "", `${item.product_name} · ${item.size} · ${item.color}`),
      el("span", "num", `${item.quantity} × ${rupiah.format(item.price)}`),
    );
    items.append(row);
  }
  if (order.shipping) {
    const row = el("div", "item muted");
    row.append(el("span", "", `Ongkir ${order.shipping.courier} ${order.shipping.service}`), el("span", "num", rupiah.format(order.shipping.cost)));
    items.append(row);
  }
  if (order.discount > 0) {
    const row = el("div", "item muted");
    row.append(el("span", "", `Voucher ${order.voucher_code}`), el("span", "num", `−${rupiah.format(order.discount)}`));
    items.append(row);
  }
  const total = el("div", "item total");
  total.append(el("span", "", "Total"), el("strong", "num", rupiah.format(order.total)));
  items.append(total);

  const side = el("div", "side");
  const address = order.shipping_address;
  if (address) {
    side.append(el("h3", "", "Kirim ke"), el("p", "address",
      `${address.recipient} (${address.phone})\n${address.street}, ${address.district}\n${address.city}, ${address.province} ${address.postal_code}`));
  }
  if (order.shipping) side.append(el("p", "muted", `${order.shipping.courier} ${order.shipping.service} · estimasi ${order.shipping.estimate}`));
  if (order.tracking_number) {
    const resi = el("p", "resi");
    resi.append("Resi ", el("strong", "", order.tracking_number));
    side.append(resi);
  }
  const facts = el("dl", "facts");
  fact(facts, "Dipesan", order.created_at);
  fact(facts, "Dibayar", order.paid_at);
  fact(facts, "Dikemas", order.packed_at);
  fact(facts, "Dikirim", order.shipped_at);
  fact(facts, "Sampai", order.arrived_at, arrivedBy[order.arrived_by] || "");
  fact(facts, "Selesai", order.completed_at, completedBy[order.completed_by] || "");
  side.append(facts);
  body.append(items, side);
  card.append(body);

  const actions = stepControls(order, card);
  if (actions) card.append(actions);
  return card;
}

// The one next step for this order, so a step can't be skipped from here either.
function stepControls(order, card) {
  const box = el("div", "actions");
  if (order.status === "paid") {
    box.append(button("Tandai dikemas", "primary", () => advance(order, card, { status: "packed" })));
    return box;
  }
  if (order.status === "packed") {
    const form = el("form", "ship");
    form.noValidate = true;
    const label = el("label", "", "Nomor resi");
    const input = el("input");
    input.type = "text";
    input.maxLength = 40;
    input.autocomplete = "off";
    input.spellcheck = false;
    input.placeholder = "mis. JP1234567890";
    input.setAttribute("aria-describedby", `ship-hint-${order.id}`);
    label.append(input);
    const send = el("button", "primary", "Tandai dikirim");
    send.type = "submit";
    const hint = el("p", "hint", "6–40 huruf, angka, atau tanda hubung.");
    hint.id = `ship-hint-${order.id}`;
    form.append(label, send, hint);
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      const tracking = input.value.trim().toUpperCase();
      if (!/^[A-Z0-9-]{6,40}$/.test(tracking)) {
        hint.textContent = "Nomor resi harus 6–40 huruf, angka, atau tanda hubung.";
        hint.classList.add("error");
        input.focus();
        return;
      }
      advance(order, card, { status: "shipped", tracking_number: tracking });
    });
    box.append(form);
    return box;
  }
  // Later this comes from the courier's tracking; until then an admin checks it.
  if (order.status === "shipped") {
    box.append(button("Tandai sudah sampai", "primary", () => advance(order, card, { status: "arrived" })),
      el("p", "hint", "Cek resi di situs kurir dulu. Nanti langkah ini diisi otomatis dari API kurir."));
    return box;
  }
  // The customer confirms it, or it completes by itself after three days.
  if (order.status === "arrived") {
    const note = el("p", "hint");
    note.append("Menunggu pembeli menekan ", el("strong", "", "Pesanan diterima"), ".");
    if (order.auto_complete_at) note.append(" Selesai otomatis ", el("strong", "", when(order.auto_complete_at)), ".");
    box.append(note);
    return box;
  }
  return null;
}

async function advance(order, card, change) {
  for (const control of card.querySelectorAll("button, input")) control.disabled = true;
  try {
    const updated = await post(`/orders/${order.id}/status`, change);
    const name = statusNames[updated.status][0];
    if (filter && updated.status !== filter) {
      // It now belongs to another tab.
      shown = shown.filter((item) => item.id !== order.id);
      card.remove();
      const moved = `Pesanan #${order.id} sekarang ${tabNames[updated.status] || name}.`;
      $("orders-status").textContent = moved;
      if (shown.length === 0) loadOrders(true, moved);
    } else {
      shown = shown.map((item) => (item.id === order.id ? updated : item));
      const fresh = orderCard(updated);
      card.replaceWith(fresh);
      $("orders-status").textContent = `Pesanan #${order.id} sekarang ${name}.`;
    }
  } catch (error) {
    if (handleAuth(error)) return;
    const messages = {
      409: `Pesanan #${order.id} sudah diubah di tempat lain. Daftar dimuat ulang.`,
      400: "Isian belum benar. Periksa nomor resinya.",
      404: `Pesanan #${order.id} tidak ditemukan.`,
    };
    const message = messages[error.status] || "Status belum bisa diubah. Coba lagi sebentar lagi.";
    $("orders-status").textContent = message;
    if (error.status === 409 || error.status === 404) {
      loadOrders(true, message);
      return;
    }
    for (const control of card.querySelectorAll("button, input")) control.disabled = false;
  }
}

// products.js loads after this file, so starting waits for both.
document.addEventListener("DOMContentLoaded", start);
