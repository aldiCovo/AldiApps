"use strict";

// The customer's account: the menu under their name in the header, and the page at #akun
// with orders, favorites, reviews, vouchers, profile, address and password. It runs after
// app.js and uses its helpers ($, el, api, productCard, the order dialog and so on).

const accountSections = ["pesanan", "favorit", "ulasan", "voucher", "biodata", "alamat", "keamanan"];

// Order statuses grouped the way customers think of them.
const orderGroups = [
  { id: "semua", label: "Semua", statuses: [] },
  { id: "bayar", label: "Menunggu bayar", statuses: ["pending", "awaiting_payment", "paying"] },
  { id: "dikemas", label: "Dikemas", statuses: ["paid", "packed"] },
  { id: "dikirim", label: "Dikirim", statuses: ["shipped"] },
  { id: "sampai", label: "Sudah sampai", statuses: ["arrived"] },
  { id: "selesai", label: "Selesai", statuses: ["completed"] },
  { id: "batal", label: "Dibatalkan", statuses: ["cancelled", "rejected"] },
];
const stripKinds = [
  { group: "bayar", label: "Menunggu bayar", path: "M4 7h15v12H4z M4 7l11-3v3 M15 13h2" },
  { group: "dikemas", label: "Dikemas", path: "M4 8l8-4 8 4v8l-8 4-8-4z M4 8l8 4 8-4 M12 12v8" },
  { group: "dikirim", label: "Dikirim", path: "M2.5 6h11v10h-11z M13.5 10h4l3 3v3h-7 M7 19.5a1.8 1.8 0 1 0 0-3.6 1.8 1.8 0 0 0 0 3.6z M17 19.5a1.8 1.8 0 1 0 0-3.6 1.8 1.8 0 0 0 0 3.6z" },
  { group: "sampai", label: "Sudah sampai", path: "M4 11l8-6 8 6v9H4z M9 15l2 2 4-4" },
];
const icons = {
  bag: "M5 8h14l-1.2 12H6.2z M9 8V6a3 3 0 0 1 6 0v2",
  heart: "M12 20s-7.5-4.6-9.2-9.4C1.6 7.2 3.8 4 7.2 4c2 0 3.6 1.1 4.8 2.8C13.2 5.1 14.8 4 16.8 4c3.4 0 5.6 3.2 4.4 6.6C19.5 15.4 12 20 12 20z",
  star: "M12 3.5l2.6 5.3 5.9.9-4.2 4.1 1 5.8L12 16.9 6.7 19.6l1-5.8-4.2-4.1 5.9-.9z",
  ticket: "M3 7h18v3a2 2 0 0 0 0 4v3H3v-3a2 2 0 0 0 0-4z M14 7v2 M14 11v2 M14 15v2",
  gear: "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z M19.4 13.5l1.6 1.2-2 3.4-1.9-.7a7 7 0 0 1-2 1.1L14.8 20.5h-4l-.3-2a7 7 0 0 1-2-1.1l-1.9.7-2-3.4 1.6-1.2a7 7 0 0 1 0-2.4L4.6 9.9l2-3.4 1.9.7a7 7 0 0 1 2-1.1l.3-2h4l.3 2a7 7 0 0 1 2 1.1l1.9-.7 2 3.4-1.6 1.2a7 7 0 0 1 0 2.4z",
  logout: "M10 5H5v14h5 M14 8l4 4-4 4 M18 12H9",
  right: "M9 6l6 6-6 6",
  down: "M6 9l6 6 6-6",
  eye: "M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z",
  eyeOff: "M3 3l18 18 M10.6 5.1A10 10 0 0 1 12 5c6.4 0 10 7 10 7a17 17 0 0 1-3.2 4 M6.6 6.6C3.9 8.4 2 12 2 12s3.6 7 10 7c1.9 0 3.6-.6 5-1.5 M9.9 9.9a3 3 0 0 0 4.2 4.2",
  camera: "M4 8h3l2-3h6l2 3h3v11H4z M12 17a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7z",
};
// The photo is made small and square here, so the upload is quick and the shop stores little.
const photoSide = 320;
const maxPhotoFile = 5 * 1024 * 1024;
const maxPhotoUpload = 45 * 1024;
const ordersPerPage = 5;
// The order list is rate limited, so it's read again only after this long or after a change.
const ordersFreshFor = 30000;
const phoneLayout = window.matchMedia("(max-width: 820px)");
const shortDate = new Intl.DateTimeFormat("id-ID", { day: "numeric", month: "short", year: "numeric" });
const monthYear = new Intl.DateTimeFormat("id-ID", { month: "long", year: "numeric" });

const account = {
  userId: null,
  orders: null,
  ordersAt: 0,
  ordersRequest: null,
  ordersFailed: false,
  group: "semua",
  page: 1,
  query: "",
  reviewTab: "wait",
  // The vouchers this customer can see, null until read.
  vouchers: null,
  vouchersRequest: null,
  vouchersFailed: false,
  voucherTab: "usable",
  address: undefined,
  provinces: false,
  favRequest: 0,
  picksLoaded: false,
  section: "",
};
const accountPicks = createPicks("acc-picks");

const icon = (path, className = "line-icon") => {
  const svg = chevron(path);
  svg.setAttribute("class", className);
  return svg;
};
const initialOf = (name) => (Array.from((name || "").trim())[0] || "?").toUpperCase();

// The customer's photo over their initial. The initial stays when there is none or it can't load.
function fillAvatar(node, user) {
  node.replaceChildren(initialOf(user.name));
  if (user.photo_version) addPhoto(node, `/api/me/photo?v=${user.photo_version}`, "avatar-photo");
  return node;
}
const firstName = (name) => (name || "").trim().split(/\s+/)[0] || "Akun";

function groupOf(order) {
  const group = orderGroups.find((g) => g.statuses.includes(order.status));
  return group ? group.id : "semua";
}

function groupCounts(orders) {
  const counts = Object.fromEntries(orderGroups.map((g) => [g.id, 0]));
  for (const order of orders || []) counts[groupOf(order)] += 1;
  return counts;
}

// One entry per product in each completed order, with the customer's review if written.
function reviewEntries(orders) {
  const entries = [];
  for (const order of orders || []) {
    if (order.status !== "completed") continue;
    const seen = new Set();
    for (const item of order.items) {
      if (seen.has(item.product_id)) continue;
      seen.add(item.product_id);
      const variants = order.items.filter((other) => other.product_id === item.product_id).map((other) => `${other.size} · ${other.color}`);
      entries.push({ order, item, variants: variants.join(", "), review: (order.reviews && order.reviews[item.product_id]) || null });
    }
  }
  return entries;
}

function loadAccountOrders(force = false) {
  if (!loggedIn) return Promise.resolve(null);
  if (!force && account.orders && Date.now() - account.ordersAt < ordersFreshFor) return Promise.resolve(account.orders);
  if (account.ordersRequest) return account.ordersRequest;
  const request = api("/orders?limit=100")
    .then(({ orders }) => {
      account.orders = orders;
      account.ordersAt = Date.now();
      account.ordersFailed = false;
      return orders;
    })
    .catch((error) => {
      account.ordersFailed = true;
      if (error.status === 401) renderAccount(null);
      throw error;
    })
    .finally(() => {
      if (account.ordersRequest === request) account.ordersRequest = null;
    });
  account.ordersRequest = request;
  return request;
}

// Reads the customer's vouchers again; calls made meanwhile share the one request.
function loadAccountVouchers() {
  if (account.vouchersRequest) return account.vouchersRequest;
  const userId = account.userId;
  const request = api("/vouchers/mine")
    .then(({ vouchers }) => {
      if (account.userId !== userId) return account.vouchers;
      account.vouchers = vouchers || [];
      account.vouchersFailed = false;
      return account.vouchers;
    }, (error) => {
      if (account.userId === userId) account.vouchersFailed = true;
      throw error;
    })
    .finally(() => {
      if (account.vouchersRequest === request) account.vouchersRequest = null;
    });
  account.vouchersRequest = request;
  return request;
}

const usableVouchers = () => (account.vouchers || []).filter((voucher) => voucher.state === "usable");

// The one beside the menu: a voucher just for this customer first, then the soonest to end.
function promoVoucher() {
  const usable = usableVouchers();
  return usable.find((voucher) => voucher.for_you) || usable[0] || null;
}

// "Menunggu bayar", "Dikemas", "Dikirim" and "Sudah sampai", each with how many orders wait there.
function statusStrip(orders) {
  const counts = groupCounts(orders);
  return stripKinds.map((kind) => {
    const link = el("a", "strip-item");
    link.href = `#akun/pesanan/${kind.group}`;
    const art = el("span", "strip-icon");
    art.append(icon(kind.path));
    const count = counts[kind.group];
    if (count > 0) art.append(el("span", "strip-count", String(count)));
    link.append(art, el("span", "strip-label", kind.label));
    if (orders) link.setAttribute("aria-label", `${kind.label}, ${count} pesanan`);
    return link;
  });
}

// The header: the customer's initial and first name, opening a short menu.
function accountMenu(user) {
  const wrap = el("div", "account-menu");
  const trigger = el("button", "account-trigger");
  trigger.type = "button";
  trigger.id = "account-trigger";
  trigger.setAttribute("aria-expanded", "false");
  trigger.setAttribute("aria-controls", "account-dropdown");
  const avatar = fillAvatar(el("span", "avatar"), user);
  avatar.setAttribute("aria-hidden", "true");
  const name = el("span", "account-first", firstName(user.name));
  name.prepend(el("span", "visually-hidden", "Akun "));
  trigger.append(avatar, name, icon(icons.down, "line-icon small"));
  const panel = el("div", "account-dropdown");
  panel.id = "account-dropdown";
  panel.hidden = true;
  trigger.addEventListener("click", () => (panel.hidden ? openMenu() : closeMenu()));
  wrap.append(trigger, panel);
  return wrap;
}

function menuLink(href, path, label, note, noteId) {
  const link = el("a", "menu-link");
  link.href = href;
  link.append(icon(path), el("span", "menu-label", label));
  const extra = el("span", "menu-note", note);
  if (noteId) extra.id = noteId;
  link.append(extra);
  return link;
}

function fillMenu() {
  const panel = $("account-dropdown");
  if (!panel || !currentUser) return;
  const profile = el("a", "menu-profile");
  profile.href = "#akun/biodata";
  const avatar = fillAvatar(el("span", "avatar large"), currentUser);
  avatar.setAttribute("aria-hidden", "true");
  const text = el("span", "profile-text");
  text.append(el("span", "profile-name", currentUser.name), el("span", "profile-email", currentUser.email));
  profile.append(avatar, text, icon(icons.right, "line-icon small"));

  const orders = el("div", "menu-orders");
  const head = el("div", "strip-head");
  const all = el("a", "", "Lihat semua");
  all.href = "#akun/pesanan";
  head.append(el("span", "", "Pesanan saya"), all);
  const strip = el("div", "status-strip");
  strip.id = "menu-strip";
  strip.append(...statusStrip(account.orders));
  orders.append(head, strip);

  const links = el("div", "menu-links");
  links.append(
    menuLink("#akun/favorit", icons.heart, "Favorit", `${favorites.length} produk`),
    menuLink("#akun/ulasan", icons.star, "Ulasan", "", "menu-review-note"),
    menuLink("#akun/voucher", icons.ticket, "Voucher saya", ""),
    menuLink("#akun/biodata", icons.gear, "Pengaturan akun", ""),
  );
  const out = el("button", "menu-link menu-logout");
  out.type = "button";
  out.append(icon(icons.logout), el("span", "menu-label", "Keluar"));
  out.addEventListener("click", logout);
  panel.replaceChildren(profile, orders, links, el("hr"), out);
  updateMenuCounts();
}

function updateMenuCounts() {
  const strip = $("menu-strip");
  if (strip && account.orders) strip.replaceChildren(...statusStrip(account.orders));
  const note = $("menu-review-note");
  if (note && account.orders) {
    const waiting = reviewEntries(account.orders).filter((entry) => !entry.review).length;
    note.textContent = waiting > 0 ? `${waiting} menunggu` : "";
    note.classList.toggle("accent", waiting > 0);
  }
}

function openMenu() {
  const panel = $("account-dropdown");
  if (!panel) return;
  fillMenu();
  panel.hidden = false;
  $("account-trigger").setAttribute("aria-expanded", "true");
  loadAccountOrders().then(updateMenuCounts, () => {});
}

function closeMenu(returnFocus = false) {
  const panel = $("account-dropdown");
  if (!panel || panel.hidden) return;
  panel.hidden = true;
  $("account-trigger").setAttribute("aria-expanded", "false");
  if (returnFocus) $("account-trigger").focus();
}

async function logout() {
  try {
    await post("/logout", {});
  } catch {
    setStatus("Belum bisa keluar. Coba lagi sebentar lagi.", true);
    $("akun-guest-text").textContent = "Belum bisa keluar. Coba lagi sebentar lagi.";
    return;
  }
  closeMenu();
  renderAccount(null);
  if (location.hash.startsWith("#akun")) location.hash = "";
}

// Called by renderAccount in app.js whenever the session changes or is read again.
function accountChanged(user) {
  const id = user ? user.id : null;
  if (id !== account.userId) {
    account.userId = id;
    account.orders = null;
    account.ordersAt = 0;
    account.addresses = null;
    account.addressCountAsked = false;
    account.vouchers = null;
    account.vouchersFailed = false;
    account.voucherTab = "usable";
    account.group = "semua";
    account.page = 1;
  }
  if (!user) closeMenu();
  routeAccount();
}

function showGuest(text, canLogin) {
  $("akun-layout").hidden = true;
  $("akun-guest").hidden = false;
  $("akun-guest-text").textContent = text;
  $("akun-login").hidden = !canLogin;
}

// Shows the account page while the address is #akun, #akun/<section> or
// #akun/pesanan/<group>, and the shop otherwise.
function routeAccount() {
  const match = location.hash.match(/^#akun(?:\/([a-z]+)(?:\/([a-z]+))?)?$/);
  const page = $("akun");
  const wasShown = !page.hidden;
  document.body.classList.toggle("on-account", Boolean(match));
  page.hidden = !match;
  if (!match) {
    if (wasShown) {
      const target = location.hash.length > 1 && document.getElementById(location.hash.slice(1));
      if (target) target.scrollIntoView();
      else window.scrollTo(0, 0);
    }
    return;
  }
  closeMenu();
  if (!wasShown) window.scrollTo(0, 0);
  if (!account.picksLoaded) {
    account.picksLoaded = true;
    shopReady.then(() => accountPicks.load()).then(() => accountPicks.refresh());
  } else {
    accountPicks.refresh();
  }
  if (!accountKnown) {
    showGuest("Memuat akunmu…", false);
    return;
  }
  if (!loggedIn) {
    showGuest("Masuk untuk melihat pesanan, favorit, dan alamatmu.", true);
    return;
  }
  $("akun-guest").hidden = true;
  $("akun-layout").hidden = false;
  let section = accountSections.includes(match[1]) ? match[1] : "";
  // On a phone, #akun is the menu itself; on a wide screen it opens the orders.
  if (!section && !phoneLayout.matches) section = "pesanan";
  if (section === "pesanan" && match[2] && orderGroups.some((g) => g.id === match[2])) {
    account.group = match[2];
    account.page = 1;
  }
  const changed = section !== account.section || !wasShown;
  account.section = section;
  $("akun-layout").classList.toggle("at-home", !section);
  for (const link of document.querySelectorAll(".account-nav [data-section]")) {
    if (link.dataset.section === section) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
  for (const name of accountSections) $(`akun-${name}`).hidden = name !== section;
  fillProfile();
  refreshSide();
  if (section) showSection(section);
  if (changed && section && phoneLayout.matches) $(`akun-${section}`).scrollIntoView();
}

function fillProfile() {
  const user = currentUser;
  for (const id of ["akun-avatar", "akun-bio-avatar"]) fillAvatar($(id), user);
  $("akun-photo-remove").hidden = !user.photo_version;
  $("akun-name").textContent = user.name;
  $("akun-email").textContent = user.email;
  const since = user.created_at ? new Date(user.created_at) : null;
  $("akun-bio-since").textContent = since ? `Member sejak ${monthYear.format(since)}` : "";
  $("akun-since").textContent = since ? `Member sejak ${monthYear.format(since)}` : "";
  $("akun-security-text").textContent = `Kami akan mengirim link ke ${user.email}. Buka link itu untuk membuat password baru.`;
}

function setNavCount(name, value, accent = false) {
  const node = document.querySelector(`.account-nav [data-count="${name}"]`);
  node.hidden = !value;
  node.textContent = value ? String(value) : "";
  node.classList.toggle("accent", accent);
}

// The menu's counts, the shopping figures and the voucher card beside the page.
function refreshSide() {
  const drawFavorites = () => {
    setNavCount("favorit", favorites.length);
    $("akun-stat-favs").textContent = counted.format(favorites.length);
  };
  drawFavorites();
  syncAccountFavorites().then(drawFavorites, () => {});
  const drawOrders = () => {
    const orders = account.orders;
    if (!orders) return;
    const counts = groupCounts(orders);
    const open = counts.bayar + counts.dikemas + counts.dikirim + counts.sampai;
    setNavCount("pesanan", open, true);
    const entries = reviewEntries(orders);
    const waiting = entries.filter((entry) => !entry.review).length;
    setNavCount("ulasan", waiting, true);
    $("akun-stat-orders").textContent = counted.format(orders.length);
    $("akun-stat-reviews").textContent = counted.format(entries.length - waiting);
    $("akun-strip").replaceChildren(...statusStrip(orders));
    updateMenuCounts();
  };
  drawOrders();
  if (!account.orders) $("akun-strip").replaceChildren(...statusStrip(null));
  loadAccountOrders().then(() => {
    drawOrders();
    if (account.section === "pesanan") renderOrdersPanel();
    if (account.section === "ulasan") renderReviewsPanel();
  }, () => {
    if (account.section === "pesanan") renderOrdersPanel();
    if (account.section === "ulasan") renderReviewsPanel();
  });
  if (account.addresses) setNavCount("alamat", account.addresses.addresses.length);
  else if (!account.addressCountAsked) {
    account.addressCountAsked = true;
    api("/me/addresses").then((book) => {
      if (!account.addresses) account.addresses = book;
      setNavCount("alamat", account.addresses.addresses.length);
    }, () => {});
  }
  const drawVouchers = () => {
    if (!account.vouchers) return;
    setNavCount("voucher", usableVouchers().length);
    renderPromo(promoVoucher());
  };
  drawVouchers();
  loadAccountVouchers().then(() => {
    drawVouchers();
    if (account.section === "voucher") renderVoucherPanel();
  }, () => {
    if (account.section === "voucher") renderVoucherPanel();
  });
}

function showSection(section) {
  if (section === "pesanan") renderOrdersPanel();
  if (section === "ulasan") renderReviewsPanel();
  if (section === "favorit") loadAccountFavorites();
  if (section === "voucher") renderVoucherPanel();
  if (section === "alamat") loadAddress();
  if (section === "biodata") fillBioForm();
  if (section === "keamanan") {
    $("akun-security-status").textContent = "";
    $("akun-password-status").textContent = "";
  }
}

// Opens one order in the order dialog, where it can be paid, cancelled, received or reviewed.
async function openOrderDetail(id) {
  detailOrder = id;
  $("orders-status").textContent = "";
  $("orders-dialog").showModal();
  await refreshOrders();
  if (!$("orders-dialog").open || shownOrders.some((order) => order.id === id)) return;
  // Older than the dialog's own list: fetched on its own.
  try {
    const order = await api(`/orders/${id}`);
    detailOrder = id;
    renderOrders([order, ...shownOrders]);
  } catch {
    $("orders-status").textContent = "Pesanan ini belum bisa dibuka. Coba lagi sebentar lagi.";
  }
}

// Order items carry no category, so look the product up in the catalog
// to show its photo or its category's photo.
const accountCatalog = new Map();
shopReady.then(catalogSample).then((products) => {
  for (const product of products) accountCatalog.set(product.id, product);
  if (!document.body.classList.contains("on-account")) return;
  if (account.section === "pesanan") renderOrdersPanel();
  if (account.section === "ulasan") renderReviewsPanel();
}).catch(() => {});

function orderThumb(item, className = "acc-thumb") {
  const box = el("span", `${className} tone-${item.product_id % 5}`);
  box.setAttribute("aria-hidden", "true");
  const product = accountCatalog.get(item.product_id);
  const photos = product ? photosOf(product) : state.productPhotos[item.product_id];
  if (!photos || !photos.length || !addPhoto(box, photos[0], "thumb-photo", () => { box.textContent = initialOf(item.product_name); })) {
    box.textContent = initialOf(item.product_name);
  }
  return box;
}

function primaryAction(order) {
  if (order.status === "awaiting_payment") return "Bayar sekarang";
  if (order.status === "arrived") return "Pesanan diterima";
  if (order.status === "completed" && order.items.some((item) => !(order.reviews && order.reviews[item.product_id]))) return "Tulis ulasan";
  return "";
}

function accountOrderCard(order) {
  const card = el("article", "acc-order");
  const head = el("div", "acc-order-head");
  const [label, tone] = orderState(order);
  head.append(icon(icons.bag), el("strong", "", "Belanja"), el("span", "", shortDate.format(new Date(order.created_at))),
    el("span", `order-state ${tone}`, label), el("span", "acc-order-id", `Pesanan #${order.id}`));
  const body = el("div", "acc-order-body");
  const first = order.items[0];
  const info = el("div", "acc-order-info");
  info.append(el("p", "acc-order-name", first.product_name),
    el("p", "acc-order-meta", `${first.size} · ${first.color} · ${first.quantity} × ${rupiah.format(first.price)}`));
  if (order.items.length > 1) info.append(el("p", "acc-order-meta", `+${order.items.length - 1} produk lainnya`));
  const total = el("div", "acc-order-total");
  total.append(el("span", "", "Total belanja"), el("strong", "", rupiah.format(order.total)));
  body.append(orderThumb(first), info, total);
  const foot = el("div", "acc-order-foot");
  foot.append(linkButton("Lihat detail", `acc-detail-${order.id}`, () => openOrderDetail(order.id)));
  const action = primaryAction(order);
  // Beli lagi is the main button only when there is nothing else to do with the order.
  if (canBuyAgain(order)) {
    foot.append(buyAgainButton(order, action ? "secondary small" : "primary small", (text) => {
      const status = $("akun-order-status");
      status.textContent = text;
      if (text) status.scrollIntoView({ block: "nearest" });
    }));
  }
  if (action) {
    const button = el("button", "primary small", action);
    button.type = "button";
    button.addEventListener("click", () => openOrderDetail(order.id));
    foot.append(button);
  }
  card.append(head, body, foot);
  return card;
}

function emptyBox(title, text, linkText, href) {
  const box = el("div", "empty-products account-empty");
  box.append(el("strong", "", title), el("span", "", text));
  if (linkText) {
    const link = el("a", "primary small", linkText);
    link.href = href;
    box.append(link);
  }
  return box;
}

function renderOrdersPanel() {
  const status = $("akun-order-status");
  const orders = account.orders;
  if (!orders) {
    $("akun-order-list").replaceChildren();
    $("akun-order-pager").hidden = true;
    status.textContent = account.ordersFailed ? "Pesanan belum bisa dimuat. Coba lagi sebentar lagi." : "Memuat pesanan…";
    return;
  }
  status.textContent = "";
  const counts = groupCounts(orders);
  const focused = $("akun-order-tabs").contains(document.activeElement) ? document.activeElement.dataset.group : null;
  $("akun-order-tabs").replaceChildren(...orderGroups.map((group) => {
    const count = counts[group.id];
    const chip = el("button", "chip", group.id !== "semua" && count > 0 ? `${group.label} (${count})` : group.label);
    chip.type = "button";
    chip.dataset.group = group.id;
    chip.setAttribute("aria-pressed", String(group.id === account.group));
    chip.addEventListener("click", () => {
      account.group = group.id;
      account.page = 1;
      renderOrdersPanel();
    });
    return chip;
  }));
  if (focused) $("akun-order-tabs").querySelector(`[data-group="${focused}"]`).focus();
  const query = account.query.trim().toLowerCase().replace(/^#/, "");
  const shown = orders.filter((order) => (account.group === "semua" || groupOf(order) === account.group)
    && (!query || String(order.id) === query || order.items.some((item) => item.product_name.toLowerCase().includes(query))));
  const pages = Math.max(1, Math.ceil(shown.length / ordersPerPage));
  account.page = Math.min(account.page, pages);
  const start = (account.page - 1) * ordersPerPage;
  const visible = shown.slice(start, start + ordersPerPage);
  if (visible.length > 0) $("akun-order-list").replaceChildren(...visible.map(accountOrderCard));
  else if (query) $("akun-order-list").replaceChildren(emptyBox("Pesanan tidak ditemukan", "Coba nama produk atau nomor pesanan lain."));
  else $("akun-order-list").replaceChildren(emptyBox("Belum ada pesanan di sini", "Yuk, cari koleksi favoritmu.", "Mulai belanja", "#katalog"));
  $("akun-order-pager").hidden = pages <= 1;
  if (pages > 1) {
    $("akun-order-pages").replaceChildren(...pagerItems(account.page, pages, (n) => {
      account.page = n;
      renderOrdersPanel();
      $("akun-pesanan").scrollIntoView();
    }));
    $("akun-order-range").textContent = `Menampilkan ${start + 1}–${start + visible.length} dari ${shown.length} pesanan`;
  }
}

function reviewEntryRow(entry) {
  const row = el("article", "acc-review");
  const info = el("div", "acc-review-info");
  const done = entry.order.completed_at || entry.order.updated_at;
  info.append(el("p", "acc-order-name", entry.item.product_name), el("p", "acc-order-meta", `${entry.variants} · Selesai ${shortDate.format(new Date(done))}`));
  if (entry.review) {
    const stars = el("p", "acc-review-stars");
    stars.append(starRow(entry.review.rating, 16), el("span", "visually-hidden", `${entry.review.rating} dari 5 bintang`));
    info.append(stars);
    if (entry.review.body) info.append(el("p", "acc-review-body", entry.review.body));
  }
  const button = el("button", entry.review ? "secondary small" : "primary small", entry.review ? "Ubah ulasan" : "Tulis ulasan");
  button.type = "button";
  button.setAttribute("aria-label", `${entry.review ? "Ubah ulasan" : "Tulis ulasan"} ${entry.item.product_name}`);
  button.addEventListener("click", () => openOrderDetail(entry.order.id));
  row.append(orderThumb(entry.item, "acc-thumb small"), info, button);
  return row;
}

function renderReviewsPanel() {
  const status = $("akun-review-status");
  if (!account.orders) {
    $("akun-review-list").replaceChildren();
    status.textContent = account.ordersFailed ? "Ulasan belum bisa dimuat. Coba lagi sebentar lagi." : "Memuat…";
    return;
  }
  const entries = reviewEntries(account.orders);
  const waiting = entries.filter((entry) => !entry.review);
  const done = entries.filter((entry) => entry.review);
  $("akun-review-wait").textContent = `Menunggu diulas (${waiting.length})`;
  $("akun-review-done").textContent = `Riwayat ulasan (${done.length})`;
  $("akun-review-wait").setAttribute("aria-pressed", String(account.reviewTab === "wait"));
  $("akun-review-done").setAttribute("aria-pressed", String(account.reviewTab === "done"));
  const shown = account.reviewTab === "wait" ? waiting : done;
  $("akun-review-list").replaceChildren(...shown.map(reviewEntryRow));
  status.textContent = shown.length > 0 ? ""
    : account.reviewTab === "wait" ? "Tidak ada produk yang menunggu ulasanmu." : "Kamu belum menulis ulasan.";
}

async function loadAccountFavorites() {
  const request = ++account.favRequest;
  const status = $("akun-fav-status");
  if ($("akun-fav-list").children.length === 0) status.textContent = "Memuat favorit…";
  if (!(await syncAccountFavorites())) {
    if (request === account.favRequest) status.textContent = "Favorit belum bisa dimuat. Coba lagi sebentar lagi.";
    return;
  }
  const ids = [...favorites];
  const results = await Promise.allSettled(ids.map((id) => api(`/products/${id}`)));
  if (request !== account.favRequest) return;
  const products = results.filter((result) => result.status === "fulfilled").map((result) => result.value);
  $("akun-fav-list").replaceChildren(...products.map(productCard));
  status.textContent = products.length > 0 ? `${products.length} produk yang kamu simpan` : "Belum ada favorit. Ketuk ikon hati di produk untuk menyimpannya di sini.";
}

async function copyCode(button, code) {
  try {
    await navigator.clipboard.writeText(code);
    button.textContent = "Tersalin ✓";
  } catch {
    button.textContent = `Kode: ${code}`;
  }
  setTimeout(() => { button.textContent = "Salin kode"; }, 2000);
}

function voucherTerms(voucher) {
  const parts = [];
  if (voucher.min_subtotal > 0) parts.push(`Min. belanja ${rupiah.format(voucher.min_subtotal)}`);
  if (voucher.ends_at) parts.push(`berlaku s.d. ${shortDate.format(new Date(voucher.ends_at))}`);
  return parts.join(" · ");
}

// What happened to a voucher that can no longer be used.
const pastVoucherText = {
  used: "Sudah dipakai",
  used_up: "Kuota habis",
  ended: "Berakhir",
};

function voucherCard(voucher) {
  const usable = voucher.state === "usable";
  const card = el("article", usable ? "voucher-card" : "voucher-card is-past");
  const big = voucher.kind === "percent" ? `${voucher.value}%` : voucher.kind === "amount" ? `Rp${Math.round(voucher.value / 1000)}rb` : "Ongkir";
  const stub = el("span", "voucher-stub", big);
  stub.setAttribute("aria-hidden", "true");
  const body = el("div", "voucher-body");
  const head = el("p", "voucher-head");
  head.append(el("span", voucher.for_you ? "voucher-badge for-you" : "voucher-badge", voucher.for_you ? "Khusus kamu" : "Untuk semua"));
  if (!usable) head.append(el("span", "voucher-state", pastVoucherText[voucher.state] || "Tidak berlaku"));
  body.append(head);
  body.append(el("p", "voucher-title", voucher.description || voucherSummary(voucher)));
  if (voucher.description) body.append(el("p", "voucher-rule", voucherSummary(voucher)));
  if (voucher.min_subtotal > 0) body.append(el("p", "voucher-rule", `Min. belanja ${rupiah.format(voucher.min_subtotal)}`));
  const ends = shortDate.format(new Date(voucher.ends_at));
  if (usable) {
    const left = voucher.uses_left > 1 ? `Bisa dipakai ${voucher.uses_left}x lagi · ` : "";
    body.append(el("p", "voucher-rule", `${left}Berlaku sampai ${ends}`));
  } else if (voucher.state === "ended") {
    body.append(el("p", "voucher-rule", `Berlaku sampai ${ends}`));
  }
  if (!usable && voucher.last_order_id > 0) {
    const used = el("p", "voucher-rule");
    const link = el("button", "link", `pesanan #${voucher.last_order_id}`);
    link.type = "button";
    link.addEventListener("click", () => openOrderDetail(voucher.last_order_id));
    used.append(document.createTextNode("Dipakai di "), link);
    body.append(used);
  }
  const row = el("div", "voucher-code-row");
  row.append(el("code", "voucher-code", voucher.code));
  if (usable) {
    const actions = el("span", "voucher-actions");
    const copy = el("button", "link", "Salin kode");
    copy.type = "button";
    copy.setAttribute("aria-label", `Salin kode ${voucher.code}`);
    copy.addEventListener("click", () => copyCode(copy, voucher.code));
    const shop = el("a", "voucher-shop", "Belanja");
    shop.href = "#";
    actions.append(copy, shop);
    row.append(actions);
  }
  body.append(row);
  card.append(stub, body);
  return card;
}

function renderVoucherPanel() {
  const status = $("akun-voucher-status");
  if (!account.vouchers) {
    $("akun-voucher-list").replaceChildren();
    status.textContent = account.vouchersFailed ? "Voucher belum bisa dimuat. Coba lagi sebentar lagi." : "Memuat voucher…";
    return;
  }
  const usable = usableVouchers();
  const past = account.vouchers.filter((voucher) => voucher.state !== "usable");
  $("akun-voucher-usable").textContent = `Bisa dipakai (${usable.length})`;
  $("akun-voucher-past").textContent = `Sudah dipakai & berakhir (${past.length})`;
  $("akun-voucher-usable").setAttribute("aria-pressed", String(account.voucherTab === "usable"));
  $("akun-voucher-past").setAttribute("aria-pressed", String(account.voucherTab === "past"));
  const shown = account.voucherTab === "usable" ? usable : past;
  $("akun-voucher-list").replaceChildren(...shown.map(voucherCard));
  status.textContent = shown.length > 0 ? ""
    : account.voucherTab === "usable" ? "Belum ada voucher yang bisa kamu pakai saat ini."
    : "Belum ada voucher yang sudah kamu pakai atau berakhir dalam 30 hari terakhir.";
}

// The voucher card beside the menu, on a category photo once the photos are known.
function renderPromo(voucher) {
  $("akun-promo").hidden = !voucher;
  if (!voucher) return;
  $("akun-promo-title").textContent = voucher.description || voucherSummary(voucher);
  $("akun-promo-terms").textContent = voucherTerms(voucher);
  shopReady.then(() => {
    const art = $("akun-promo-art");
    const photo = Object.values(state.categoryPhotos)[0];
    if (photo && art.children.length === 0) addPhoto(art, photo, "promo-photo");
  });
}

function addressLines(address) {
  return [`${address.recipient} · ${address.phone}`, address.street, `${address.district}, ${address.city}`, `${address.province} ${address.postal_code}`];
}

function addressCard(address) {
  const card = el("article", address.main ? "address-card is-main" : "address-card");
  const head = el("p", "address-card-head");
  head.append(el("span", "address-tag", address.label));
  if (address.main) head.append(el("span", "badge-main", "Utama"));
  const text = el("p", "address-text");
  text.append(...addressLines(address).map((line) => el("span", "", line)));
  const actions = el("div", "address-card-actions");
  const button = (label, onClick, extra = "") => {
    const node = el("button", `link ${extra}`.trim(), label);
    node.type = "button";
    node.addEventListener("click", onClick);
    return node;
  };
  const edit = button("Ubah", () => openAddressDialog(address));
  edit.setAttribute("aria-label", `Ubah alamat ${address.label}`);
  actions.append(edit);
  if (!address.main) {
    const main = button("Jadikan utama", () => changeAddresses(main, () => send("PUT", `/me/addresses/${address.id}/main`), `${address.label} sekarang alamat utama.`));
    const remove = button("Hapus", () => confirmRemove(), "muted");
    remove.setAttribute("aria-label", `Hapus alamat ${address.label}`);
    const confirmRemove = () => {
      const ask = el("div", "address-confirm");
      ask.setAttribute("role", "group");
      ask.setAttribute("aria-label", "Hapus alamat");
      const yes = el("button", "secondary small", "Hapus");
      yes.type = "button";
      const no = button("Batal", () => {
        ask.replaceWith(actions);
        remove.focus();
      });
      yes.addEventListener("click", () => changeAddresses(yes, () => send("DELETE", `/me/addresses/${address.id}`), `Alamat ${address.label} dihapus.`));
      ask.append(el("span", "", `Hapus alamat ${address.label}?`), yes, no);
      actions.replaceWith(ask);
      no.focus();
    };
    actions.append(main, remove);
  }
  card.append(head, text, actions);
  return card;
}

function renderAddresses() {
  const book = account.addresses;
  const count = book.addresses.length;
  setNavCount("alamat", count);
  $("akun-address-add").hidden = count >= book.limit;
  $("akun-address-note").hidden = false;
  $("akun-address-note").textContent = count === 0
    ? "Belum ada alamat tersimpan. Tambahkan supaya checkout lebih cepat."
    : `${count} dari ${book.limit} alamat. Alamat utama dipilih otomatis saat checkout.`;
  $("akun-address-list").replaceChildren(...book.addresses.map(addressCard));
}

// Sends one change to the list, then shows the list the shop answered with.
async function changeAddresses(button, request, done) {
  button.disabled = true;
  const status = $("akun-address-status");
  status.classList.remove("error");
  status.textContent = "";
  try {
    account.addresses = await request();
    renderAddresses();
    status.textContent = done;
  } catch (error) {
    if (error.status === 401) {
      renderAccount(null);
      return;
    }
    button.disabled = false;
    status.textContent = error.status === 404
      ? "Alamat ini sudah tidak ada. Daftarnya dimuat ulang."
      : "Alamat belum bisa diubah. Coba lagi sebentar lagi.";
    status.classList.add("error");
    if (error.status === 404) loadAddress();
  }
}

async function loadProvinceChoices() {
  if (account.provinces) return;
  const table = shippingTable || await api("/shipping");
  if (account.provinces) return;
  account.provinces = true;
  $("akun-address-province").append(...table.provinces.map(({ name }) => {
    const option = el("option", "", name);
    option.value = name;
    return option;
  }));
}

async function loadAddress() {
  const status = $("akun-address-status");
  status.classList.remove("error");
  if (!account.addresses) {
    status.textContent = "Memuat alamat…";
    $("akun-address-note").hidden = true;
    $("akun-address-list").replaceChildren();
  }
  try {
    const [book] = await Promise.all([api("/me/addresses"), loadProvinceChoices()]);
    account.addresses = book;
    status.textContent = "";
    renderAddresses();
  } catch (error) {
    if (error.status === 401) {
      renderAccount(null);
      return;
    }
    if (!account.addresses) status.textContent = "Alamat belum bisa dimuat. Coba lagi sebentar lagi.";
  }
}

function setAddressLabel(value) {
  $("akun-address-label").value = value;
  const chosen = ["Rumah", "Kantor"].includes(value) ? value : "";
  for (const chip of document.querySelectorAll(".address-label [data-label]")) {
    chip.setAttribute("aria-pressed", String(chip.dataset.label === chosen));
  }
}

// Opens the form for a new address, or for changing the given one.
function openAddressDialog(address) {
  const book = account.addresses;
  const first = !book || book.addresses.length === 0;
  account.editingAddress = address || null;
  $("akun-address-dialog-title").textContent = address ? "Ubah alamat" : "Tambah alamat";
  const values = address || { label: first ? "Rumah" : "", recipient: currentUser ? currentUser.name : "", phone: currentUser ? currentUser.phone || "" : "" };
  setAddressLabel(values.label || "");
  for (const field of addressFields) $(`akun-address-${field}`).value = values[field] || "";
  if (![...$("akun-address-province").options].some((option) => option.value && option.value === values.province)) $("akun-address-province").value = "";
  // The main address stays main until another is chosen; the first one is main anyway.
  $("akun-address-main-row").hidden = first || Boolean(address && address.main);
  $("akun-address-main").checked = false;
  $("akun-address-error").hidden = true;
  account.addressOpener = document.activeElement;
  $("akun-address-dialog").showModal();
  (address ? $("akun-address-recipient") : $("akun-address-label")).focus();
}

async function submitAccountAddress(event) {
  event.preventDefault();
  const form = $("akun-address-form");
  $("akun-address-error").hidden = true;
  if (!form.reportValidity()) return;
  const saveButton = $("akun-address-save");
  saveButton.disabled = true;
  const editing = account.editingAddress;
  try {
    const body = Object.fromEntries(addressFields.map((field) => [field, $(`akun-address-${field}`).value]));
    body.label = $("akun-address-label").value.trim();
    body.main = $("akun-address-main").checked;
    account.addresses = editing
      ? await send("PUT", `/me/addresses/${editing.id}`, body)
      : await send("POST", "/me/addresses", body);
    $("akun-address-dialog").close();
    renderAddresses();
    $("akun-address-status").classList.remove("error");
    $("akun-address-status").textContent = editing ? "Alamat tersimpan." : "Alamat ditambahkan.";
  } catch (error) {
    if (error.status === 401) {
      $("akun-address-dialog").close();
      renderAccount(null);
      return;
    }
    $("akun-address-error").textContent = error.status === 400
      ? "Periksa lagi alamatnya. Nomor HP harus nomor ponsel Indonesia dan kode pos 5 angka."
      : error.status === 409 ? "Sudah ada 10 alamat. Hapus salah satu dulu."
      : error.status === 404 ? "Alamat ini sudah tidak ada. Tutup lalu buka lagi daftarnya."
      : "Alamat belum bisa disimpan. Coba lagi sebentar lagi.";
    $("akun-address-error").hidden = false;
  } finally {
    saveButton.disabled = false;
  }
}

async function sendPasswordLink() {
  const button = $("akun-security-send");
  button.disabled = true;
  $("akun-security-status").textContent = "";
  try {
    await post("/password/forgot", { email: currentUser.email });
    $("akun-security-status").textContent = `Link sudah dikirim ke ${currentUser.email}. Cek kotak masuk, juga folder spam.`;
  } catch (error) {
    $("akun-security-status").textContent = authMessage(error);
  } finally {
    button.disabled = false;
  }
}

// After the profile changes: the header menu and the page show the new name and photo.
function profileChanged(profile) {
  currentUser = profile;
  const wasOpen = $("account-dropdown") && !$("account-dropdown").hidden;
  $("account").replaceChildren(accountMenu(currentUser));
  if (wasOpen) openMenu();
  fillProfile();
}

function fillBioForm() {
  const user = currentUser;
  $("akun-bio-name").value = user.name;
  $("akun-bio-birth").value = user.birth_date || "";
  $("akun-bio-birth").max = new Date().toISOString().slice(0, 10);
  $("akun-bio-phone").value = user.phone || "";
  $("akun-bio-email").value = user.email;
  setGender(user.gender || "");
  $("akun-bio-error").hidden = true;
  $("akun-bio-status").textContent = "";
}

function setGender(value) {
  for (const chip of document.querySelectorAll(".bio-gender [data-gender]")) {
    chip.setAttribute("aria-pressed", String(chip.dataset.gender === value));
  }
}

function chosenGender() {
  const chip = document.querySelector('.bio-gender [aria-pressed="true"]');
  return chip ? chip.dataset.gender : "";
}

function showFormError(id, text) {
  $(id).textContent = text;
  $(id).hidden = false;
}

async function submitBio(event) {
  event.preventDefault();
  const name = $("akun-bio-name").value.trim();
  $("akun-bio-error").hidden = true;
  $("akun-bio-status").textContent = "";
  if (!name) {
    showFormError("akun-bio-error", "Nama lengkap wajib diisi.");
    $("akun-bio-name").focus();
    return;
  }
  const button = $("akun-bio-save");
  button.disabled = true;
  try {
    const profile = await send("PUT", "/me", {
      name,
      phone: $("akun-bio-phone").value.trim(),
      birth_date: $("akun-bio-birth").value,
      gender: chosenGender(),
    });
    profileChanged(profile);
    fillBioForm();
    $("akun-bio-status").textContent = "Biodata tersimpan.";
  } catch (error) {
    if (error.status === 401) {
      renderAccount(null);
      return;
    }
    showFormError("akun-bio-error", error.status === 400
      ? "Periksa lagi isinya. Nomor HP harus nomor ponsel Indonesia, dan tanggal lahir sebelum hari ini."
      : "Biodata belum bisa disimpan. Coba lagi sebentar lagi.");
  } finally {
    button.disabled = false;
  }
}

function blobAsBase64(blob) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.addEventListener("load", () => resolve(String(reader.result).split(",")[1]));
    reader.addEventListener("error", () => reject(reader.error));
    reader.readAsDataURL(blob);
  });
}

// Cuts the middle square out of the picture and makes it photoSide wide, as a JPEG small
// enough for the shop to accept.
async function squarePhoto(file) {
  const bitmap = await createImageBitmap(file);
  const side = Math.min(bitmap.width, bitmap.height);
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = photoSide;
  const context = canvas.getContext("2d");
  context.fillStyle = "#fff";
  context.fillRect(0, 0, photoSide, photoSide);
  context.drawImage(bitmap, (bitmap.width - side) / 2, (bitmap.height - side) / 2, side, side, 0, 0, photoSide, photoSide);
  bitmap.close();
  for (const quality of [0.86, 0.75, 0.6, 0.45]) {
    const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/jpeg", quality));
    if (blob && blob.size <= maxPhotoUpload) return blob;
  }
  throw new Error("photo too large");
}

async function uploadPhoto(event) {
  const file = event.target.files[0];
  event.target.value = "";
  if (!file) return;
  const status = $("akun-photo-status");
  status.classList.remove("error");
  if (!["image/jpeg", "image/png", "image/webp"].includes(file.type) || file.size > maxPhotoFile) {
    status.textContent = "Pilih foto JPG, PNG atau WEBP, maksimal 5 MB.";
    status.classList.add("error");
    return;
  }
  const pick = $("akun-photo-pick");
  pick.disabled = true;
  status.textContent = "Mengunggah foto…";
  try {
    let blob;
    try {
      blob = await squarePhoto(file);
    } catch {
      status.textContent = "Foto ini belum bisa dibaca. Coba foto lain.";
      status.classList.add("error");
      return;
    }
    profileChanged(await send("PUT", "/me/photo", { image: await blobAsBase64(blob) }));
    status.textContent = "Foto profil tersimpan.";
  } catch (error) {
    if (error.status === 401) {
      renderAccount(null);
      return;
    }
    status.textContent = "Foto belum bisa disimpan. Coba lagi sebentar lagi.";
    status.classList.add("error");
  } finally {
    pick.disabled = false;
  }
}

async function removePhoto() {
  const status = $("akun-photo-status");
  status.classList.remove("error");
  const button = $("akun-photo-remove");
  button.disabled = true;
  try {
    await send("DELETE", "/me/photo");
    profileChanged({ ...currentUser, photo_version: 0 });
    status.textContent = "Foto profil dihapus.";
    $("akun-photo-pick").focus();
  } catch (error) {
    if (error.status === 401) {
      renderAccount(null);
      return;
    }
    status.textContent = "Foto belum bisa dihapus. Coba lagi sebentar lagi.";
    status.classList.add("error");
  } finally {
    button.disabled = false;
  }
}

async function submitPassword(event) {
  event.preventDefault();
  const current = $("akun-password-current");
  const next = $("akun-password-new");
  const repeat = $("akun-password-repeat");
  $("akun-password-error").hidden = true;
  $("akun-password-status").textContent = "";
  const problem = !current.value ? [current, "Isi password sekarang."]
    : Array.from(next.value).length < 8 ? [next, "Password baru minimal 8 karakter."]
    : next.value !== repeat.value ? [repeat, "Ulangi password baru belum sama."]
    : null;
  if (problem) {
    showFormError("akun-password-error", problem[1]);
    problem[0].focus();
    return;
  }
  const button = $("akun-password-save");
  button.disabled = true;
  try {
    await send("PUT", "/me/password", { current_password: current.value, new_password: next.value });
    for (const input of [current, next, repeat]) input.value = "";
    $("akun-password-status").textContent = "Password sudah diganti.";
  } catch (error) {
    if (error.status === 401) {
      renderAccount(null);
      return;
    }
    if (error.status === 403) {
      showFormError("akun-password-error", "Password sekarang salah.");
      current.focus();
      return;
    }
    showFormError("akun-password-error", error.status === 400
      ? "Password baru minimal 8 karakter."
      : "Password belum bisa diganti. Coba lagi sebentar lagi.");
  } finally {
    button.disabled = false;
  }
}

for (const toggle of document.querySelectorAll(".password-toggle")) {
  toggle.append(icon(icons.eye, "line-icon small"));
  toggle.addEventListener("click", () => {
    const input = $(toggle.getAttribute("aria-controls"));
    const shown = input.type === "password";
    input.type = shown ? "text" : "password";
    toggle.setAttribute("aria-pressed", String(shown));
    toggle.setAttribute("aria-label", shown ? "Sembunyikan password" : "Tampilkan password");
    toggle.replaceChildren(icon(shown ? icons.eyeOff : icons.eye, "line-icon small"));
  });
}
for (const chip of document.querySelectorAll(".bio-gender [data-gender]")) {
  // Pressing the chosen one again leaves it empty.
  chip.addEventListener("click", () => setGender(chip.getAttribute("aria-pressed") === "true" ? "" : chip.dataset.gender));
}
$("akun-bio-form").addEventListener("submit", submitBio);
$("akun-photo-pick").addEventListener("click", () => $("akun-photo-input").click());
$("akun-photo-input").addEventListener("change", uploadPhoto);
$("akun-photo-remove").addEventListener("click", removePhoto);
$("akun-password-form").addEventListener("submit", submitPassword);

$("akun-login").addEventListener("click", () => openAuth());
$("akun-logout").addEventListener("click", logout);
$("akun-order-search").addEventListener("input", (event) => {
  account.query = event.target.value;
  account.page = 1;
  renderOrdersPanel();
});
$("akun-review-wait").addEventListener("click", () => { account.reviewTab = "wait"; renderReviewsPanel(); });
$("akun-review-done").addEventListener("click", () => { account.reviewTab = "done"; renderReviewsPanel(); });
$("akun-voucher-usable").addEventListener("click", () => { account.voucherTab = "usable"; renderVoucherPanel(); });
$("akun-voucher-past").addEventListener("click", () => { account.voucherTab = "past"; renderVoucherPanel(); });
$("akun-address-add").addEventListener("click", () => openAddressDialog(null));
for (const chip of document.querySelectorAll(".address-label [data-label]")) {
  chip.addEventListener("click", () => {
    setAddressLabel(chip.dataset.label);
    if (!chip.dataset.label) $("akun-address-label").focus();
  });
}
$("akun-address-label").addEventListener("input", (event) => {
  const value = event.target.value.trim();
  for (const chip of document.querySelectorAll(".address-label [data-label]")) {
    chip.setAttribute("aria-pressed", String(chip.dataset.label === (["Rumah", "Kantor"].includes(value) ? value : "")));
  }
});
$("akun-address-dialog").addEventListener("close", () => {
  const opener = account.addressOpener;
  account.addressOpener = null;
  if (opener && opener.isConnected) opener.focus();
  else $("akun-address-add").focus();
});
$("akun-address-form").addEventListener("submit", submitAccountAddress);
$("akun-security-send").addEventListener("click", sendPasswordLink);
$("akun-promo-copy").addEventListener("click", () => {
  const voucher = promoVoucher();
  if (voucher) copyCode($("akun-promo-copy"), voucher.code);
});
// Paying, receiving or reviewing in the order dialog changes the orders shown here.
$("orders-dialog").addEventListener("close", () => {
  if (!loggedIn) return;
  loadAccountOrders(true).then(() => {
    if (!$("akun").hidden) refreshSide();
  }, () => {});
});
window.addEventListener("hashchange", routeAccount);
phoneLayout.addEventListener("change", () => { if (!$("akun").hidden) routeAccount(); });
// Clicking outside the account menu, or pressing Escape, closes it. So does following a link.
document.addEventListener("pointerdown", (event) => {
  const menu = document.querySelector(".account-menu");
  if (menu && !menu.contains(event.target)) closeMenu();
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && $("account-dropdown") && !$("account-dropdown").hidden) closeMenu(true);
});
document.addEventListener("focusin", (event) => {
  const menu = document.querySelector(".account-menu");
  if (menu && !menu.contains(event.target)) closeMenu();
});

// Should /me have answered before this file ran, the header and page are drawn now.
if (accountKnown && currentUser) $("account").replaceChildren(accountMenu(currentUser));
routeAccount();

// Opening the page at #akun makes the browser jump to the section, under the
// sticky header. The account page fills the screen, so start at the top.
window.addEventListener("load", () => {
  if (document.body.classList.contains("on-account")) window.scrollTo(0, 0);
});
