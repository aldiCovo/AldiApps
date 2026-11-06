"use strict";

const pageSize = 24;
const $ = (id) => document.getElementById(id);
const rupiah = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });

const state = { q: "", categoryId: "", size: "", color: "", inStock: false, offset: 0, total: 0, categories: new Map(), categoryPhotos: {}, productPhotos: {} };
let latestRequest = 0;

class ApiError extends Error {
  constructor(status, body = null) {
    super(`request failed with status ${status}`);
    this.status = status;
    // The service's JSON answer, such as why a voucher was refused.
    this.body = body;
  }
}

async function api(path, options = {}) {
  const response = await fetch(`/api${path}`, options);
  if (!response.ok) throw new ApiError(response.status, await response.json().catch(() => null));
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

// Drawings stand in until products have photos. A category without one shows the first letter.
const garments = { dress: "garment-dress", blouse: "garment-blouse", rok: "garment-rok", hijab: "garment-hijab" };

function fillArt(node, categoryName, fallback) {
  const garment = garments[(categoryName || "").trim().toLowerCase()];
  if (!garment) {
    node.textContent = Array.from(fallback)[0] || "";
    return;
  }
  const svgNS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(svgNS, "svg");
  svg.setAttribute("class", "garment");
  svg.setAttribute("viewBox", "0 0 120 150");
  const use = document.createElementNS(svgNS, "use");
  use.setAttribute("href", `#${garment}`);
  svg.append(use);
  node.replaceChildren(svg);
}

function categoryPhotoURL(categoryName) {
  return state.categoryPhotos[(categoryName || "").trim().toLowerCase().replace(/\s+/g, "-")];
}

// A product's own photos, or else its category's photo, or none.
function photosOf(product) {
  const own = state.productPhotos[product.id];
  if (own && own.length) return own;
  const category = categoryPhotoURL(state.categories.get(product.category_id));
  return category ? [category] : [];
}

// Puts the category's photo over the drawing. If it can't load, the drawing stays.
function addCategoryPhoto(node, categoryName, className, onError) {
  return addPhoto(node, categoryPhotoURL(categoryName), className, onError);
}

function addPhoto(node, url, className, onError) {
  if (!url) {
    return false;
  }
  const image = document.createElement("img");
  image.className = className;
  image.src = url;
  image.alt = "";
  image.loading = "lazy";
  image.decoding = "async";
  image.addEventListener("error", () => {
    image.remove();
    if (onError) {
      onError();
    }
  });
  node.append(image);
  return true;
}

// A product shows its first photo, or its category's photo until it has its own.
function fillSwatch(node, product, className = "art") {
  node.className = `${className} tone-${product.id % 5}`;
  fillArt(node, state.categories.get(product.category_id), product.name);
  addPhoto(node, photosOf(product)[0], "art-photo");
}

// Buttons around a quantity. They're out of the tab order because the field takes arrow keys.
function addStepper(input) {
  const stepper = el("div", "stepper");
  input.before(stepper);
  const step = (label, text, change) => {
    const button = el("button", "", text);
    button.type = "button";
    button.tabIndex = -1;
    button.setAttribute("aria-label", label);
    button.addEventListener("click", () => {
      change();
      input.dispatchEvent(new Event("change"));
    });
    return button;
  };
  stepper.append(step("Kurangi", "−", () => input.stepDown()), input, step("Tambah", "+", () => input.stepUp()));
}

function productCard(product) {
  const card = el("button", "card");
  card.type = "button";
  const art = el("span", "card-art");
  const swatch = el("span");
  swatch.setAttribute("aria-hidden", "true");
  fillSwatch(swatch, product);
  art.append(swatch);
  const stock = totalStock(product);
  if (stock === 0) art.append(el("span", "badge sold-out", "Stok habis"));
  else if (stock <= 5) art.append(el("span", "badge", `Sisa ${stock}`));
  art.append(el("span", "card-view", "Lihat detail"));
  card.append(art);
  const category = state.categories.get(product.category_id);
  if (category) card.append(el("span", "card-category", category));
  card.append(el("span", "card-name", product.name));
  const sizes = [...new Set((product.variants || []).map((variant) => variant.size))];
  if (sizes.length > 0) card.append(el("span", "card-meta", `Ukuran ${sizes.join(", ")}`));
  card.append(el("span", "card-price", rupiah.format(product.price)));
  card.addEventListener("click", () => openProduct(product.id));
  return card;
}

function showSkeletons() {
  if ($("products").children.length > 0) return;
  $("products").append(...Array.from({ length: 8 }, () => el("div", "skeleton")));
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
  if (!append) {
    setStatus("Memuat produk…");
    showSkeletons();
  }
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
  const photos = api("/category-photos").catch(() => ({}));
  const productPhotos = api("/product-photos").catch(() => ({}));
  try {
    const { categories } = await api("/categories");
    state.categories = new Map(categories.map((category) => [category.id, category.name]));
  } catch {
    state.categories = new Map();
  }
  state.categoryPhotos = await photos;
  state.productPhotos = await productPhotos;
  renderCategories();
}

function renderCategories() {
  const choose = (id) => {
    state.categoryId = id;
    renderCategories();
    loadProducts();
  };
  const chip = (label, id) => {
    const button = el("button", "chip", label);
    button.type = "button";
    button.setAttribute("aria-pressed", String(state.categoryId === id));
    button.addEventListener("click", () => choose(id));
    return button;
  };
  const tile = (id, name, index) => {
    const button = el("button", "tile");
    button.type = "button";
    const art = el("span", `tile-art tone-${index % 5}`);
    art.setAttribute("aria-hidden", "true");
    fillArt(art, name, name);
    const label = el("span", "tile-label");
    label.append(el("span", "tile-name", name));
    if (addCategoryPhoto(art, name, "tile-photo", () => button.classList.remove("has-photo"))) {
      button.classList.add("has-photo");
      label.append(el("span", "tile-cta", "Lihat koleksi"));
    }
    button.append(art, label);
    button.addEventListener("click", () => {
      choose(String(id));
      $("katalog").scrollIntoView();
    });
    return button;
  };
  const categories = [...state.categories];
  $("categories").replaceChildren(chip("Semua", ""), ...categories.map(([id, name]) => chip(name, String(id))));
  $("category-tiles").replaceChildren(...categories.map(([id, name], index) => tile(id, name, index)));
  $("shop-by").hidden = categories.length === 0;
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
  row.append(input, el("span", "variant-name", `${variant.size} · ${variant.color}`),
    el("span", soldOut ? "variant-stock sold-out" : "variant-stock", soldOut ? "Stok habis" : `Stok ${variant.stock}`));
  return row;
}

let shownProductId = null;

async function openProduct(id) {
  try {
    const product = await api(`/products/${encodeURIComponent(id)}`);
    fillSwatch($("product-swatch"), product);
    showProductPhotos(product);
    const category = state.categories.get(product.category_id);
    $("product-category").textContent = category || "";
    $("product-category").hidden = !category;
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

// The photo viewer. A click or tap zooms in on that spot and zooms out again, dragging moves the
// photo, and a mouse wheel or two fingers zoom gradually. Photos are only a few hundred pixels
// wide for now, so zooming further than this only shows blur.
const zoomStep = 2.5;
const zoomMax = 4;
// The photos of the product on screen, and which one is showing.
let shownPhotos = [];
let shownPhoto = 0;
let shownName = "";

function showProductPhotos(product) {
  shownPhotos = photosOf(product);
  shownPhoto = 0;
  shownName = product.name;
  const thumbs = shownPhotos.map((url, index) => {
    const button = el("button", "thumb");
    button.type = "button";
    button.setAttribute("aria-label", `Foto ${index + 1} dari ${shownPhotos.length}`);
    const image = el("img");
    image.src = url;
    image.alt = "";
    image.decoding = "async";
    button.append(image);
    button.addEventListener("click", () => selectPhoto(index));
    return button;
  });
  $("product-thumbs").replaceChildren(...thumbs);
  $("product-thumbs").hidden = thumbs.length < 2;
  showZoomButton(product.name);
  selectPhoto(0);
}

// Shows one of the product's photos in the sheet.
function selectPhoto(index) {
  shownPhoto = index;
  const photo = $("product-swatch").querySelector(".art-photo");
  if (photo && shownPhotos[index] && photo.getAttribute("src") !== shownPhotos[index]) photo.src = shownPhotos[index];
  $("product-thumbs").querySelectorAll(".thumb").forEach((thumb, i) => thumb.setAttribute("aria-pressed", String(i === index)));
}

const viewer = { scale: 1, x: 0, y: 0, width: 0, height: 0, pointers: new Map(), drag: null, pinch: null, moved: false };

function showZoomButton(name) {
  const photo = $("product-swatch").querySelector(".art-photo");
  $("photo-zoom").hidden = !photo;
  $("photo-zoom").setAttribute("aria-label", `Perbesar foto ${name}`);
  $("photo-image").alt = name;
  if (photo) photo.addEventListener("error", () => { $("photo-zoom").hidden = true; });
}

function openPhoto() {
  if (!shownPhotos.length) return;
  $("photo-dialog").showModal();
  viewPhoto(shownPhoto);
}

// Shows a photo in the viewer, starting fitted. From the last one, next goes back to the first.
function viewPhoto(index) {
  const count = shownPhotos.length;
  shownPhoto = (index + count) % count;
  const image = $("photo-image");
  const url = shownPhotos[shownPhoto];
  image.alt = count > 1 ? `${shownName}, foto ${shownPhoto + 1} dari ${count}` : shownName;
  $("photo-count").textContent = `${shownPhoto + 1} / ${count}`;
  for (const id of ["photo-prev", "photo-next", "photo-count"]) $(id).hidden = count < 2;
  if (image.getAttribute("src") !== url) {
    // Hidden until the new photo has loaded and been fitted, so the old one doesn't jump.
    image.style.visibility = "hidden";
    image.src = url;
  }
  if (image.complete && image.naturalWidth) fitPhoto();
}

// Sizes the photo to fit the screen, which is zoom level 1.
function fitPhoto() {
  const image = $("photo-image");
  const stage = $("photo-stage");
  if (!image.naturalWidth || !stage.clientWidth) return;
  const ratio = Math.min(stage.clientWidth / image.naturalWidth, stage.clientHeight / image.naturalHeight);
  viewer.width = image.naturalWidth * ratio;
  viewer.height = image.naturalHeight * ratio;
  image.style.width = `${viewer.width}px`;
  image.style.height = `${viewer.height}px`;
  image.style.visibility = "";
  viewer.scale = 1;
  viewer.x = 0;
  viewer.y = 0;
  showZoom();
}

// Keeps the photo's edges from coming inside the screen while zoomed.
function showZoom() {
  const stage = $("photo-stage");
  const limitX = Math.max(0, (viewer.width * viewer.scale - stage.clientWidth) / 2);
  const limitY = Math.max(0, (viewer.height * viewer.scale - stage.clientHeight) / 2);
  viewer.x = Math.min(limitX, Math.max(-limitX, viewer.x));
  viewer.y = Math.min(limitY, Math.max(-limitY, viewer.y));
  $("photo-image").style.transform = `translate(${viewer.x}px, ${viewer.y}px) scale(${viewer.scale})`;
  const zoomed = viewer.scale > 1.01;
  stage.classList.toggle("zoomed", zoomed);
  $("photo-zoom-out").disabled = !zoomed;
  $("photo-zoom-in").disabled = viewer.scale >= zoomMax;
  const touch = matchMedia("(hover: none)").matches;
  $("photo-hint").textContent = zoomed
    ? (touch ? "Geser untuk melihat detail" : "Geser untuk melihat detail, klik untuk kembali")
    : (touch ? "Ketuk foto untuk memperbesar" : "Klik foto untuk memperbesar");
}

// Zooms while the spot under the point, measured from the screen's centre, stays put.
function zoomAt(scale, pointX = 0, pointY = 0) {
  const next = Math.min(zoomMax, Math.max(1, scale));
  const factor = next / viewer.scale;
  viewer.x = pointX - (pointX - viewer.x) * factor;
  viewer.y = pointY - (pointY - viewer.y) * factor;
  viewer.scale = next;
  showZoom();
}

function stagePoint(event) {
  const box = $("photo-stage").getBoundingClientRect();
  return { x: event.clientX - box.left - box.width / 2, y: event.clientY - box.top - box.height / 2 };
}

function pinchSpan() {
  const [a, b] = [...viewer.pointers.values()];
  return { distance: Math.hypot(a.x - b.x, a.y - b.y) || 1, x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
}

function startDrag(point) {
  viewer.last = point;
  viewer.drag = { x: point.x, y: point.y, fromX: viewer.x, fromY: viewer.y };
}

function photoPointerDown(event) {
  if (event.pointerType === "mouse" && event.button !== 0) return;
  event.preventDefault();
  $("photo-stage").setPointerCapture(event.pointerId);
  const point = stagePoint(event);
  viewer.pointers.set(event.pointerId, point);
  if (viewer.pointers.size === 1) {
    viewer.moved = false;
    viewer.pinched = false;
    startDrag(point);
  } else if (viewer.pointers.size === 2) {
    viewer.moved = true;
    viewer.pinched = true;
    viewer.pinch = pinchSpan();
    $("photo-stage").classList.add("dragging");
  }
}

function photoPointerMove(event) {
  if (!viewer.pointers.has(event.pointerId)) return;
  const point = stagePoint(event);
  viewer.pointers.set(event.pointerId, point);
  if (viewer.pointers.size >= 2 && viewer.pinch) {
    const span = pinchSpan();
    zoomAt(viewer.scale * (span.distance / viewer.pinch.distance), span.x, span.y);
    viewer.x += span.x - viewer.pinch.x;
    viewer.y += span.y - viewer.pinch.y;
    showZoom();
    viewer.pinch = span;
    return;
  }
  viewer.last = point;
  const dx = point.x - viewer.drag.x;
  const dy = point.y - viewer.drag.y;
  if (!viewer.moved && Math.hypot(dx, dy) > 6) {
    viewer.moved = true;
    $("photo-stage").classList.add("dragging");
  }
  if (viewer.moved && viewer.scale > 1) {
    viewer.x = viewer.drag.fromX + dx;
    viewer.y = viewer.drag.fromY + dy;
    showZoom();
  }
}

function photoPointerUp(event) {
  if (!viewer.pointers.has(event.pointerId)) return;
  const point = viewer.pointers.get(event.pointerId);
  viewer.pointers.delete(event.pointerId);
  if (viewer.pointers.size === 1) {
    // One finger stays after a pinch: it carries on dragging from where it is.
    viewer.pinch = null;
    startDrag([...viewer.pointers.values()][0]);
    return;
  }
  if (viewer.pointers.size > 0) return;
  $("photo-stage").classList.remove("dragging");
  // A sideways swipe on a photo that isn't zoomed moves to the next or previous one.
  const dx = viewer.last.x - viewer.drag.x;
  const dy = viewer.last.y - viewer.drag.y;
  if (event.type === "pointerup" && viewer.moved && !viewer.pinched && viewer.scale <= 1.01 && shownPhotos.length > 1 && Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy)) {
    viewPhoto(shownPhoto + (dx < 0 ? 1 : -1));
    return;
  }
  if (event.type === "pointerup" && !viewer.moved) {
    if (viewer.scale > 1.01) zoomAt(1);
    else zoomAt(zoomStep, point.x, point.y);
  }
}

function photoWheel(event) {
  event.preventDefault();
  const point = stagePoint(event);
  const delta = event.deltaMode === 1 ? event.deltaY * 16 : event.deltaY;
  zoomAt(viewer.scale * Math.exp(-delta * 0.002), point.x, point.y);
}

function photoKey(event) {
  const pan = 80;
  const moves = { ArrowLeft: [pan, 0], ArrowRight: [-pan, 0], ArrowUp: [0, pan], ArrowDown: [0, -pan] };
  if (event.key === "+" || event.key === "=") zoomAt(viewer.scale * 1.5);
  else if (event.key === "-") zoomAt(viewer.scale / 1.5);
  else if (event.key === "0") zoomAt(1);
  else if ((event.key === "ArrowLeft" || event.key === "ArrowRight") && viewer.scale <= 1.01 && shownPhotos.length > 1) {
    viewPhoto(shownPhoto + (event.key === "ArrowRight" ? 1 : -1));
  } else if (moves[event.key] && viewer.scale > 1.01) {
    viewer.x += moves[event.key][0];
    viewer.y += moves[event.key][1];
    showZoom();
  } else return;
  event.preventDefault();
}

let authMode = "login";
let loggedIn = false;
let currentUser = null;

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
  $("forgot-row").hidden = registering;
  $("auth-note").hidden = true;
  $("auth-error").hidden = true;
}

function openAuth(note = "") {
  $("auth-form").reset();
  setAuthMode("login");
  $("auth-note").textContent = note;
  $("auth-note").hidden = !note;
  $("auth-dialog").showModal();
}

function openForgot() {
  const email = $("auth-email").value.trim();
  $("auth-dialog").close();
  $("forgot-form").reset();
  $("forgot-email").value = email;
  $("forgot-form").hidden = false;
  $("forgot-done").hidden = true;
  $("forgot-error").hidden = true;
  $("forgot-dialog").showModal();
}

async function submitForgot(event) {
  event.preventDefault();
  $("forgot-submit").disabled = true;
  $("forgot-error").hidden = true;
  try {
    await post("/password/forgot", { email: $("forgot-email").value.trim() });
    $("forgot-form").hidden = true;
    $("forgot-done").hidden = false;
  } catch (error) {
    $("forgot-error").textContent = error.status === 400 ? "Periksa kembali alamat email kamu." : authMessage(error);
    $("forgot-error").hidden = false;
  } finally {
    $("forgot-submit").disabled = false;
  }
}

// The token comes in the part of the link after #, which browsers never send to a server.
// It's taken off the address bar at once, so it doesn't stay in the history.
let resetToken = "";

function openResetFromLink() {
  const match = location.hash.match(/^#reset=([A-Za-z0-9_-]+)$/);
  if (!match) return;
  resetToken = match[1];
  history.replaceState(null, "", location.pathname + location.search);
  $("reset-form").reset();
  $("reset-error").hidden = true;
  $("reset-dialog").showModal();
}

function showResetError(text) {
  $("reset-error").textContent = text;
  $("reset-error").hidden = false;
}

async function submitReset(event) {
  event.preventDefault();
  const password = $("reset-password").value;
  if (password !== $("reset-confirm").value) {
    showResetError("Kedua password tidak sama.");
    return;
  }
  $("reset-submit").disabled = true;
  $("reset-error").hidden = true;
  try {
    await post("/password/reset", { token: resetToken, password });
    resetToken = "";
    $("reset-form").reset();
    $("reset-dialog").close();
    openAuth("Password baru sudah disimpan. Silakan masuk.");
  } catch (error) {
    if (error.status === 410) showResetError("Link ini sudah tidak berlaku. Minta link baru lewat \"Lupa password?\".");
    else showResetError(authMessage(error));
  } finally {
    $("reset-submit").disabled = false;
  }
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
  const wasLoggedIn = loggedIn;
  loggedIn = Boolean(user);
  currentUser = user;
  if (!user && $("checkout-dialog").open) $("checkout-dialog").close();
  if (user) {
    if ($("cart-dialog").open) refreshCart();
    else syncAccountCart();
  } else if (wasLoggedIn) {
    cart = loadCart();
    showCartCount();
    // Only an ended session gets here with the cart open, because logging out is behind it.
    if ($("cart-dialog").open) {
      refreshCart("Silakan masuk lagi untuk melihat keranjang akunmu.");
      openAuth();
    }
  }
  const account = $("account");
  if (!user) {
    const login = el("button", "secondary", "Masuk");
    login.type = "button";
    login.addEventListener("click", () => openAuth());
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
  } catch (error) {
    // A network problem doesn't end the session, so a logged-in page stays as it is.
    if (error.status === 401 || !loggedIn) renderAccount(null);
  }
}

const orderStates = {
  pending: ["Memeriksa stok…", ""],
  awaiting_payment: ["Menunggu pembayaran", ""],
  paying: ["Memproses pembayaran…", ""],
  paid: ["Dibayar", "good"],
  packed: ["Dikemas", "good"],
  shipped: ["Dikirim", "good"],
  completed: ["Selesai", "good"],
  rejected: ["Ditolak: stok tidak cukup", "bad"],
};
const pollEvery = 1500;
// Signals may be lost, so a slow poll still runs while the connection is open.
const pollEveryWhileLive = 15000;
const liveRetryMax = 30000;
const unsettled = new Set(["pending", "paying"]);
// Orders that can still change, so the list listens for their signals. The shop moves a paid
// order on from the admin pages.
const openStatuses = new Set(["pending", "awaiting_payment", "paying", "paid", "packed", "shipped"]);
let pollTimer = null;
let shownOrders = [];
let liveSocket = null;
let liveTimer = null;
let liveRetry = 1000;

const isLive = () => liveSocket !== null && liveSocket.readyState === WebSocket.OPEN;

function orderState(order) {
  if (order.status === "cancelled") {
    const reasons = { expired: "Dibatalkan: waktu pembayaran habis", cancelled_by_customer: "Dibatalkan atas permintaanmu" };
    return [reasons[order.reason] || "Dibatalkan: pembayaran gagal", "bad"];
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

// Cancelling asks once more in the card itself, because it can't be undone. The list is drawn
// again whenever orders change, so the open question is remembered by order id.
let cancelQuestion = null;

function cancelControls(order) {
  const box = el("div", "order-cancel");
  const showAsk = () => {
    const ask = el("button", "link", "Batalkan pesanan");
    ask.type = "button";
    ask.dataset.key = `cancel-${order.id}`;
    ask.addEventListener("click", () => {
      cancelQuestion = order.id;
      showQuestion();
      box.querySelector(".secondary").focus();
    });
    box.replaceChildren(ask);
    return ask;
  };
  const showQuestion = () => {
    const question = el("p", "order-cancel-question", `Batalkan pesanan #${order.id}? Stoknya dikembalikan dan pesanan ini tidak bisa dibayar lagi.`);
    const yes = el("button", "secondary", "Ya, batalkan");
    yes.type = "button";
    yes.dataset.key = `cancel-yes-${order.id}`;
    const no = el("button", "link", "Tidak");
    no.type = "button";
    no.dataset.key = `cancel-no-${order.id}`;
    no.addEventListener("click", () => {
      cancelQuestion = null;
      showAsk().focus();
    });
    yes.addEventListener("click", async () => {
      cancelQuestion = null;
      for (const button of box.closest(".order").querySelectorAll("button")) button.disabled = true;
      try {
        await post(`/orders/${order.id}/cancel`, {});
        $("orders-status").textContent = `Pesanan #${order.id} sudah dibatalkan.`;
      } catch (error) {
        if (error.status === 401) renderAccount(null);
        $("orders-status").textContent = error.status === 409 ? "Pesanan ini sudah tidak bisa dibatalkan." : "Pesanan belum bisa dibatalkan. Coba lagi sebentar lagi.";
      }
      await refreshOrders();
    });
    const buttons = el("div", "order-cancel-buttons");
    buttons.append(yes, no);
    box.replaceChildren(question, buttons);
  };
  if (cancelQuestion === order.id) showQuestion();
  else showAsk();
  return box;
}

const dateTime = new Intl.DateTimeFormat("id-ID", { dateStyle: "medium", timeStyle: "short" });
const when = (iso) => dateTime.format(new Date(iso));
// The order opened in detail, by id, so it stays open while the list is drawn again.
let detailOrder = null;

function linkButton(label, key, onClick) {
  const button = el("button", "link", label);
  button.type = "button";
  button.dataset.key = key;
  button.addEventListener("click", onClick);
  return button;
}

// What the customer can still do with an order, the same in the list and in its detail.
function orderActions(order, target) {
  if (order.status === "rejected") {
    const again = el("button", "link", "Masukkan lagi ke keranjang");
    again.type = "button";
    again.addEventListener("click", async () => {
      again.disabled = true;
      try {
        await addToCart(order.items.map(({ product_id, variant_id, quantity }) => ({ product_id, variant_id, quantity })));
        $("orders-dialog").close();
        openCart();
      } catch {
        $("orders-status").textContent = "Keranjang belum bisa diubah. Coba lagi sebentar lagi.";
        again.disabled = false;
      }
    });
    target.append(again);
  }
  if (order.status === "awaiting_payment") {
    if (order.expires_at) {
      const time = new Date(order.expires_at).toLocaleTimeString("id-ID", { hour: "2-digit", minute: "2-digit" });
      target.append(el("p", "order-item", `Bayar sebelum pukul ${time}. Setelah itu stok dikembalikan.`));
    }
    const actions = el("div", "order-actions");
    actions.append(payButton(order, "Bayar (simulasi)", "primary", "success"), payButton(order, "Simulasikan gagal bayar", "secondary", "failure"));
    target.append(actions, cancelControls(order));
  }
}

function showOrderDetail(id) {
  detailOrder = id;
  $("orders-status").textContent = "";
  renderOrders(shownOrders);
  $("orders-dialog").scrollTop = 0;
  $("orders-list").querySelector(".detail-back").focus();
}

function showOrderList() {
  const id = detailOrder;
  detailOrder = null;
  $("orders-status").textContent = "";
  renderOrders(shownOrders);
  const link = $("orders-list").querySelector(`[data-key="detail-${id}"]`);
  if (link) link.focus();
}

function orderCard(order) {
  const card = el("article", "order");
  const head = el("div", "order-head");
  const [label, tone] = orderState(order);
  head.append(el("span", "", `Pesanan #${order.id}`), el("span", `order-state ${tone}`, label));
  card.append(head, el("p", "order-date", `Dipesan ${when(order.created_at)}`));
  for (const item of order.items) {
    card.append(el("p", "order-item", `${item.product_name} · ${item.size} · ${item.color} × ${item.quantity}`));
  }
  if (order.tracking_number) {
    const courier = order.shipping ? `${order.shipping.courier} ${order.shipping.service} · ` : "";
    card.append(el("p", "order-item order-resi", `${courier}resi ${order.tracking_number}`));
  }
  const foot = el("div", "order-foot");
  foot.append(el("p", "card-price", `Total ${rupiah.format(order.total)}`),
    linkButton(order.paid_at ? "Lihat detail & struk" : "Lihat detail", `detail-${order.id}`, () => showOrderDetail(order.id)));
  card.append(foot);
  orderActions(order, card);
  return card;
}

function sumRow(label, value, className = "sum-row") {
  const row = el("p", className);
  row.append(el("span", "", label), el(className === "cart-total" ? "strong" : "span", "", value));
  return row;
}

// One order in full. A paid one reads as its receipt and can be printed or saved as PDF.
function orderDetail(order) {
  const box = el("article", "order order-detail");
  box.append(linkButton("← Semua pesanan", "detail-back", showOrderList));
  box.lastChild.classList.add("detail-back");
  const paid = Boolean(order.paid_at);
  if (paid) {
    const brand = el("div", "receipt-brand");
    brand.append(el("span", "brand", "Prastika"), el("span", "", "Struk pembayaran"));
    box.append(brand);
  }
  const head = el("div", "order-head");
  const [label, tone] = orderState(order);
  head.append(el("h3", "detail-title", `Pesanan #${order.id}`), el("span", `order-state ${tone}`, label));
  box.append(head);

  const facts = el("dl", "detail-facts");
  const fact = (name, value) => facts.append(el("dt", "", name), el("dd", "", value));
  fact("Dipesan", when(order.created_at));
  if (order.paid_at) fact("Dibayar", when(order.paid_at));
  if (order.packed_at) fact("Dikemas", when(order.packed_at));
  if (order.shipped_at) fact("Dikirim", when(order.shipped_at));
  if (order.completed_at) fact("Selesai", when(order.completed_at));
  if (order.status === "cancelled" || order.status === "rejected") fact("Berakhir", when(order.updated_at));
  if (paid) fact("Metode", "Simulasi pembayaran");
  box.append(facts);

  const items = el("div", "detail-items");
  let subtotal = 0;
  for (const item of order.items) {
    const line = item.price * item.quantity;
    subtotal += line;
    const row = el("div", "detail-item");
    const name = el("div", "");
    name.append(el("span", "detail-item-name", item.product_name), el("span", "detail-item-meta", `${item.size} · ${item.color} · ${item.quantity} × ${rupiah.format(item.price)}`));
    row.append(name, el("span", "detail-item-sum", rupiah.format(line)));
    items.append(row);
  }
  box.append(items);

  const sums = el("div", "checkout-sums");
  sums.append(sumRow("Subtotal", rupiah.format(subtotal)));
  const shipping = order.shipping;
  if (shipping) sums.append(sumRow(`Ongkir ${shipping.courier} ${shipping.service}`, rupiah.format(shipping.cost)));
  if (order.discount > 0) sums.append(sumRow(`Voucher ${order.voucher_code}`, `−${rupiah.format(order.discount)}`, "sum-row discount-row"));
  sums.append(sumRow("Total", rupiah.format(order.total), "cart-total"));
  box.append(sums);

  if (shipping || order.shipping_address) {
    const send = el("section", "detail-send");
    send.append(el("h4", "", "Pengiriman"));
    if (shipping) send.append(el("p", "order-item", `Kurir ${shipping.courier} ${shipping.service} · estimasi ${shipping.estimate}`));
    if (order.tracking_number) send.append(el("p", "order-item order-resi", `Nomor resi ${order.tracking_number}`));
    const address = order.shipping_address;
    if (address) {
      send.append(el("p", "order-item order-address", `${address.recipient} (${address.phone})\n${address.street}, ${address.district}, ${address.city}, ${address.province} ${address.postal_code}`));
    }
    box.append(send);
  }
  if (paid) {
    box.append(el("p", "receipt-note", "Pembayaran ini masih simulasi: tidak ada uang yang ditarik dan tidak ada barang yang dikirim."));
    const print = el("button", "secondary detail-print", "Cetak / simpan PDF");
    print.type = "button";
    print.dataset.key = `print-${order.id}`;
    print.addEventListener("click", () => window.print());
    box.append(print);
  }
  orderActions(order, box);
  return box;
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
  // Drawing the list again keeps keyboard focus on the same button.
  const focused = document.activeElement;
  const focusKey = $("orders-list").contains(focused) ? focused.dataset.key : undefined;
  const detail = detailOrder !== null && orders.find((order) => order.id === detailOrder);
  if (detailOrder !== null && !detail) detailOrder = null;
  $("orders-dialog").classList.toggle("showing-detail", Boolean(detail));
  $("orders-list").replaceChildren(...(detail ? [orderDetail(detail)] : orders.map(orderCard)));
  if (focusKey) {
    const again = $("orders-list").querySelector(`[data-key="${focusKey}"]`);
    if (again) again.focus();
  }
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

// Why order-service refused a voucher, in the shop's words.
function voucherMessage(body) {
  const reason = body && body.reason;
  if (reason === "min_subtotal") return `Voucher ini butuh belanja minimal ${rupiah.format(body.min_subtotal)}.`;
  return {
    not_found: "Kode voucher tidak ditemukan.",
    not_started: "Voucher ini belum bisa dipakai. Promonya belum dimulai.",
    expired: "Masa berlaku voucher ini sudah habis.",
    used_up: "Kuota voucher ini sudah habis.",
    already_used: "Kamu sudah memakai voucher ini sebanyak yang diizinkan.",
  }[reason] || "Voucher ini tidak bisa dipakai.";
}

function orderError(error) {
  if (error.status === 422) return voucherMessage(error.body);
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
  $("product-dialog").close();
  openCheckout([{ product_id: shownProductId, variant_id: Number(chosen.value), quantity: Number($("order-quantity").value) }], false);
}

// A guest's cart lives in this browser. A customer's lives on their account in cart-service, so
// it follows them to any browser and leaves this one when they log out. Logging in moves a
// guest's cart to the account. Both hold only ids and quantities: names, prices and stock are
// read again each time the cart opens, so it never shows old ones.
const cartKey = "prastika-cart";
const cartMaxItems = 20;
const cartMaxQuantity = 20;
const isPositiveInt = (value) => Number.isSafeInteger(value) && value > 0;
let cart = loadCart();
let cartDetails = new Map();
let latestCartRequest = 0;
let cartSync = null;

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

function clearGuestCart() {
  try {
    localStorage.removeItem(cartKey);
  } catch {
    // Without storage there's nothing to clear.
  }
}

function showCartCount() {
  const count = cart.reduce((sum, item) => sum + item.quantity, 0);
  $("cart-count").textContent = count > 99 ? "99+" : count;
  $("cart-count").hidden = count === 0;
  $("cart-open").setAttribute("aria-label", `Keranjang, ${count} barang`);
}

function saveGuestCart() {
  try {
    localStorage.setItem(cartKey, JSON.stringify(cart));
  } catch {
    // Without storage the cart still works until the page closes.
  }
}

function send(method, path, body) {
  const options = { method };
  if (body !== undefined) {
    options.headers = { "Content-Type": "application/json" };
    options.body = JSON.stringify(body);
  }
  return api(path, options);
}

// Reads the account's cart, first moving a guest's cart into it. Requests share one run, so a
// guest's cart can't be added twice. The guest's copy stays until the account has it.
function syncAccountCart() {
  cartSync ||= (async () => {
    const guest = loadCart();
    try {
      const result = guest.length > 0 ? await post("/cart/merge", { items: guest }) : await api("/cart");
      if (guest.length > 0) clearGuestCart();
      if (!loggedIn) return false;
      cart = result.items;
      showCartCount();
      return true;
    } catch (error) {
      if (error.status === 401) renderAccount(null);
      return false;
    }
  })().finally(() => { cartSync = null; });
  return cartSync;
}

// Returns how many items didn't fit.
async function addToCart(items) {
  if (loggedIn) {
    const result = await post("/cart/merge", { items });
    cart = result.items;
    showCartCount();
    return result.skipped;
  }
  let skipped = 0;
  for (const added of items) {
    const item = cart.find((entry) => entry.variant_id === added.variant_id);
    if (item) item.quantity = Math.min(item.quantity + added.quantity, cartMaxQuantity);
    else if (cart.length < cartMaxItems) cart.push({ ...added, quantity: Math.min(added.quantity, cartMaxQuantity) });
    else skipped++;
  }
  saveGuestCart();
  showCartCount();
  return skipped;
}

function cartWriteMessage(error) {
  if (error.status === 429) return "Terlalu banyak percobaan. Tunggu sebentar lalu coba lagi.";
  return "Keranjang belum bisa diubah. Coba lagi sebentar lagi.";
}

async function submitCartAdd(event) {
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
  $("cart-add").disabled = true;
  try {
    const skipped = await addToCart([{ product_id: shownProductId, variant_id: variantId, quantity: Math.min(wanted, room) }]);
    if (skipped > 0) {
      $("order-error").textContent = `Keranjang penuh. Satu pesanan paling banyak ${cartMaxItems} jenis barang.`;
      $("order-error").hidden = false;
      return;
    }
    $("cart-note-text").textContent = wanted > room ? `Hanya ${room} yang bisa ditambahkan, sesuai stok.` : "Masuk ke keranjang.";
    $("cart-note").hidden = false;
  } catch (error) {
    if (error.status === 401) renderAccount(null);
    $("order-error").textContent = error.status === 401 ? "Silakan masuk lagi, lalu coba tambahkan." : cartWriteMessage(error);
    $("order-error").hidden = false;
  } finally {
    $("cart-add").disabled = false;
  }
}

// Shows a failed change, then reads the cart again so the page matches what's really saved.
async function cartWriteFailed(error) {
  if (error.status === 401) {
    renderAccount(null);
    return;
  }
  await refreshCart();
  if ($("cart-footer").hidden) {
    $("cart-status").textContent = cartWriteMessage(error);
    return;
  }
  $("cart-error").textContent = cartWriteMessage(error);
  $("cart-error").hidden = false;
}

function cartRow(item) {
  const { product, variant } = cartDetails.get(item.variant_id);
  const soldOut = variant.stock === 0;
  const row = el("article", "cart-row");
  const art = el("div");
  art.setAttribute("aria-hidden", "true");
  fillSwatch(art, product, "art cart-art");
  const about = el("div");
  about.append(el("div", "cart-name", product.name), el("div", "cart-detail", `${variant.size} · ${variant.color}`), el("div", "cart-detail", rupiah.format(product.price)));
  const subtotal = el("div", "cart-subtotal", soldOut ? "" : rupiah.format(product.price * item.quantity));
  const actions = el("div", "cart-actions");
  if (soldOut) {
    actions.append(el("span", "sold-out", "Stok habis"));
  } else {
    const input = el("input");
    input.type = "number";
    input.setAttribute("aria-label", `Jumlah ${product.name} ${variant.size} ${variant.color}`);
    input.min = 1;
    input.max = Math.min(variant.stock, cartMaxQuantity);
    input.value = item.quantity;
    input.addEventListener("change", async () => {
      const wanted = Math.trunc(Number(input.value)) || 1;
      item.quantity = Math.min(Math.max(wanted, 1), Number(input.max));
      input.value = item.quantity;
      subtotal.textContent = rupiah.format(product.price * item.quantity);
      showCartCount();
      updateCartTotal();
      if (!loggedIn) {
        saveGuestCart();
        return;
      }
      try {
        await send("PUT", `/cart/items/${item.variant_id}`, { product_id: item.product_id, quantity: item.quantity });
      } catch (error) {
        await cartWriteFailed(error);
      }
    });
    actions.append(input);
    addStepper(input);
  }
  const remove = el("button", "link", "Hapus");
  remove.type = "button";
  remove.setAttribute("aria-label", `Hapus ${product.name} ${variant.size} ${variant.color}`);
  remove.addEventListener("click", async () => {
    cart = cart.filter((entry) => entry.variant_id !== item.variant_id);
    showCartCount();
    renderCart();
    $("cart-title").focus();
    if (!loggedIn) {
      saveGuestCart();
      return;
    }
    try {
      await send("DELETE", `/cart/items/${item.variant_id}`);
    } catch (error) {
      await cartWriteFailed(error);
    }
  });
  actions.append(remove);
  row.append(art, about, subtotal, actions);
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
    $("cart-status").textContent = note || "Keranjangmu masih kosong.";
    return;
  }
  const soldOut = cart.some((item) => cartDetails.get(item.variant_id).variant.stock === 0);
  $("cart-status").textContent = [note, soldOut ? "Hapus barang yang stoknya habis untuk memesan." : ""].filter(Boolean).join(" ");
  updateCartTotal();
}

async function refreshCart(note = "") {
  const request = ++latestCartRequest;
  $("cart-error").hidden = true;
  $("cart-footer").hidden = true;
  $("cart-list").replaceChildren();
  $("cart-status").textContent = "Memuat keranjang…";
  if (loggedIn) {
    const synced = await syncAccountCart();
    if (request !== latestCartRequest) return;
    if (!synced) {
      $("cart-status").textContent = "Keranjang belum bisa dimuat. Tutup lalu buka lagi sebentar lagi.";
      return;
    }
  }
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
  const gone = cart.filter((item) => !cartDetails.has(item.variant_id));
  const reduced = cart.filter((item) => {
    const details = cartDetails.get(item.variant_id);
    return details && details.variant.stock > 0 && item.quantity > details.variant.stock;
  });
  cart = cart.filter((item) => cartDetails.has(item.variant_id));
  for (const item of reduced) item.quantity = cartDetails.get(item.variant_id).variant.stock;
  showCartCount();
  if (loggedIn) {
    // The next opening repeats these if one fails, so a failure doesn't need showing.
    Promise.allSettled([
      ...gone.map((item) => send("DELETE", `/cart/items/${item.variant_id}`)),
      ...reduced.map((item) => send("PUT", `/cart/items/${item.variant_id}`, { product_id: item.product_id, quantity: item.quantity })),
    ]);
  } else {
    saveGuestCart();
  }
  const notes = note ? [note] : [];
  if (gone.length > 0) notes.push(`${gone.length} barang dihapus karena sudah tidak dijual.`);
  if (reduced.length > 0) notes.push(`${reduced.length} barang dikurangi jumlahnya, sesuai stok.`);
  renderCart(notes.join(" "));
}

function openCart() {
  $("cart-dialog").showModal();
  refreshCart();
}

function checkout() {
  if (!loggedIn) {
    openAuth();
    return;
  }
  $("cart-dialog").close();
  openCheckout(cart, true);
}

// Checkout shows where the order goes, what's in it and the total, before it's made. Names,
// prices and stock are read again here, so they're current. The address is kept on the
// account, and each order gets its own copy of it.
const addressFields = ["recipient", "phone", "street", "district", "city", "province", "postal_code"];
let checkoutItems = [];
let checkoutFromCart = false;
let checkoutBlocked = true;
let savedAddress = null;
let latestCheckout = 0;
// Courier prices by province, from order-service, which prices the order again itself.
let shippingTable = null;
let chosenShipping = "";
let checkoutSubtotal = 0;
// The voucher the customer applied, with its terms, so the discount follows the courier.
// order-service checks it again when the order is made.
let appliedVoucher = null;

async function openCheckout(items, fromCart) {
  if (!loggedIn) {
    openAuth();
    return;
  }
  const request = ++latestCheckout;
  checkoutItems = items.map(({ product_id, variant_id, quantity }) => ({ product_id, variant_id, quantity }));
  checkoutFromCart = fromCart;
  checkoutBlocked = true;
  for (const id of ["checkout-error", "checkout-problem", "address-error"]) $(id).hidden = true;
  $("voucher-code").value = "";
  showVoucher(null);
  $("checkout-body").hidden = true;
  $("checkout-status").textContent = "Memuat…";
  $("checkout-dialog").showModal();
  const ids = [...new Set(checkoutItems.map((item) => item.product_id))];
  const [address, products, table] = await Promise.allSettled([
    api("/me/address"),
    Promise.allSettled(ids.map((id) => api(`/products/${id}`))),
    shippingTable || api("/shipping"),
  ]);
  if (request !== latestCheckout || !$("checkout-dialog").open) return;
  if (address.status === "rejected" && address.reason.status === 401) {
    renderAccount(null);
    openAuth();
    return;
  }
  const failed = products.value.some((result) => result.status === "rejected" && result.reason.status !== 404);
  if (failed || table.status === "rejected" || (address.status === "rejected" && address.reason.status !== 404)) {
    $("checkout-status").textContent = "Checkout belum bisa dimuat. Tutup lalu coba lagi sebentar lagi.";
    return;
  }
  savedAddress = address.status === "fulfilled" ? address.value : null;
  if (!shippingTable) {
    shippingTable = table.value;
    const select = $("address-province");
    select.append(...shippingTable.provinces.map(({ name }) => {
      const option = el("option", "", name);
      option.value = name;
      return option;
    }));
  }
  const variants = new Map();
  for (const result of products.value) {
    if (result.status !== "fulfilled") continue;
    for (const variant of result.value.variants || []) variants.set(variant.id, { product: result.value, variant });
  }
  renderCheckoutItems(variants);
  // An address saved before provinces came from a list may need its province picked again.
  const knownProvince = savedAddress && zoneOf(savedAddress.province);
  showAddress(!knownProvince);
  if (savedAddress && !knownProvince) {
    $("address-error").textContent = "Pilih provinsi dari daftar supaya ongkir bisa dihitung.";
    $("address-error").hidden = false;
  }
  $("checkout-status").textContent = "";
  $("checkout-body").hidden = false;
}

function renderCheckoutItems(variants) {
  let subtotal = 0;
  let problem = "";
  const rows = checkoutItems.map((item) => {
    const row = el("div", "cart-row");
    const details = variants.get(item.variant_id);
    if (!details) {
      problem = "Ada barang yang sudah tidak dijual. Hapus dulu dari keranjang.";
      row.append(el("div"), el("div", "cart-name", "Barang ini sudah tidak dijual"));
      return row;
    }
    const { product, variant } = details;
    const art = el("div");
    art.setAttribute("aria-hidden", "true");
    fillSwatch(art, product, "art cart-art");
    const about = el("div");
    about.append(el("div", "cart-name", product.name), el("div", "cart-detail", `${variant.size} · ${variant.color} · ${item.quantity} × ${rupiah.format(product.price)}`));
    if (variant.stock < item.quantity) {
      problem = "Stok ada yang kurang dari jumlah pesananmu. Ubah jumlahnya di keranjang dulu.";
      about.append(el("p", "error", variant.stock === 0 ? "Stok habis" : `Stok tinggal ${variant.stock}`));
    }
    const lineTotal = product.price * item.quantity;
    subtotal += lineTotal;
    row.append(art, about, el("div", "cart-subtotal", rupiah.format(lineTotal)));
    return row;
  });
  $("checkout-items").replaceChildren(...rows);
  checkoutSubtotal = subtotal;
  $("checkout-problem").textContent = problem;
  $("checkout-problem").hidden = !problem;
  checkoutBlocked = Boolean(problem);
  $("checkout-submit").disabled = checkoutBlocked;
}

// Shows the saved address, or the form when there's none yet or it's being changed.
function showAddress(editing) {
  $("address-form").hidden = !editing;
  $("address-view").hidden = editing;
  $("address-cancel").hidden = !(savedAddress && zoneOf(savedAddress.province));
  $("address-error").hidden = true;
  if (editing) {
    const address = savedAddress || { recipient: currentUser ? currentUser.name : "" };
    for (const field of addressFields) $(`address-${field}`).value = address[field] || "";
    // A province that isn't in the list leaves the choice empty.
    if (!zoneOf($("address-province").value)) $("address-province").value = "";
  } else {
    const a = savedAddress;
    $("address-text").replaceChildren(...[`${a.recipient} · ${a.phone}`, a.street, `${a.district}, ${a.city}`, `${a.province} ${a.postal_code}`].map((line) => el("span", "", line)));
  }
  renderShipping();
}

function zoneOf(province) {
  const found = shippingTable && shippingTable.provinces.find(({ name }) => name === province);
  return found ? found.zone : 0;
}

// The province the order would go to: the one in the form while it's open.
function checkoutProvince() {
  return $("address-form").hidden ? (savedAddress && savedAddress.province) : $("address-province").value;
}

function shippingQuote() {
  const zone = zoneOf(checkoutProvince());
  const option = zone && shippingTable.options.find(({ id }) => id === chosenShipping);
  return option ? { ...option, cost: option.costs[zone - 1], estimate: option.estimates[zone - 1] } : null;
}

// Lists each courier's price to the chosen province. The cheapest is picked until the customer
// picks one, and a pick stays when the province changes.
function renderShipping() {
  const zone = zoneOf(checkoutProvince());
  const note = $("shipping-note");
  if (!zone) {
    $("shipping-options").replaceChildren();
    note.textContent = "Pilih provinsi di alamat untuk melihat pilihan kurir.";
    note.hidden = false;
    updateCheckoutSums();
    return;
  }
  note.hidden = true;
  const options = shippingTable.options;
  if (!options.some(({ id }) => id === chosenShipping)) {
    chosenShipping = options.reduce((cheapest, option) => (option.costs[zone - 1] < cheapest.costs[zone - 1] ? option : cheapest)).id;
  }
  $("shipping-options").replaceChildren(...options.map((option) => {
    const label = el("label", "shipping-option");
    const input = el("input");
    input.type = "radio";
    input.name = "shipping";
    input.value = option.id;
    input.checked = option.id === chosenShipping;
    input.addEventListener("change", () => {
      chosenShipping = option.id;
      updateCheckoutSums();
    });
    label.append(input, el("span", "shipping-name", `${option.courier} ${option.service}`), el("span", "shipping-estimate", `Estimasi ${option.estimates[zone - 1]}`), el("span", "shipping-cost", rupiah.format(option.costs[zone - 1])));
    return label;
  }));
  updateCheckoutSums();
}

// The same sum as order-service: a share or an amount of the items, or the shipping, never
// more than either.
function voucherDiscount(voucher, subtotal, shippingCost) {
  let discount = voucher.kind === "percent" ? Math.floor(subtotal * voucher.value / 100)
    : voucher.kind === "amount" ? voucher.value : shippingCost;
  if (voucher.max_discount) discount = Math.min(discount, voucher.max_discount);
  return Math.min(discount, voucher.kind === "free_shipping" ? shippingCost : subtotal);
}

function updateCheckoutSums() {
  const quote = shippingQuote();
  const shippingCost = quote ? quote.cost : 0;
  const discount = appliedVoucher ? voucherDiscount(appliedVoucher, checkoutSubtotal, shippingCost) : 0;
  $("checkout-subtotal").textContent = rupiah.format(checkoutSubtotal);
  $("checkout-shipping").textContent = quote ? rupiah.format(quote.cost) : "—";
  $("checkout-discount-row").hidden = !appliedVoucher;
  if (appliedVoucher) {
    $("checkout-discount-label").textContent = appliedVoucher.kind === "free_shipping" ? `Gratis ongkir (${appliedVoucher.code})` : `Voucher ${appliedVoucher.code}`;
    $("checkout-discount").textContent = `−${rupiah.format(discount)}`;
  }
  $("checkout-total").textContent = rupiah.format(checkoutSubtotal + shippingCost - discount);
}

function voucherSummary(voucher) {
  const most = voucher.max_discount ? `, maksimal ${rupiah.format(voucher.max_discount)}` : "";
  if (voucher.kind === "percent") return `Diskon ${voucher.value}%${most}`;
  if (voucher.kind === "amount") return `Potongan ${rupiah.format(voucher.value)}`;
  return `Gratis ongkir${most}`;
}

// Shows the applied voucher, or the field to type one.
function showVoucher(voucher) {
  appliedVoucher = voucher;
  $("voucher-form").hidden = Boolean(voucher);
  $("voucher-applied").hidden = !voucher;
  $("voucher-error").hidden = true;
  if (voucher) {
    $("voucher-applied-code").textContent = voucher.code;
    $("voucher-applied-text").textContent = `· ${voucher.description || voucherSummary(voucher)}`;
  }
  updateCheckoutSums();
}

function voucherProblem(message) {
  $("voucher-error").textContent = message;
  $("voucher-error").hidden = false;
}

async function submitVoucher(event) {
  event.preventDefault();
  const code = $("voucher-code").value.trim();
  if (!code) {
    voucherProblem("Masukkan kode voucher dulu.");
    return;
  }
  $("voucher-apply").disabled = true;
  $("voucher-error").hidden = true;
  try {
    const quote = shippingQuote();
    const voucher = await post("/vouchers/check", {
      voucher_code: code,
      items: checkoutItems.map(({ variant_id, quantity }) => ({ variant_id, quantity })),
      shipping_option: quote ? quote.id : "",
      province: quote ? checkoutProvince() : "",
    });
    showVoucher(voucher);
  } catch (error) {
    if (error.status === 401) {
      renderAccount(null);
      openAuth();
      return;
    }
    voucherProblem(error.status === 422 ? voucherMessage(error.body)
      : error.status === 429 ? "Terlalu banyak percobaan. Tunggu sebentar lalu coba lagi."
      : "Voucher belum bisa dicek. Coba lagi sebentar lagi.");
  } finally {
    $("voucher-apply").disabled = false;
  }
}

async function saveAddress() {
  const address = Object.fromEntries(addressFields.map((field) => [field, $(`address-${field}`).value]));
  // The answer is the address as stored, with the phone number in one form.
  savedAddress = await send("PUT", "/me/address", address);
}

// Answers true when the address could be saved.
async function trySaveAddress() {
  $("address-error").hidden = true;
  try {
    await saveAddress();
    showAddress(false);
    return true;
  } catch (error) {
    if (error.status === 401) {
      renderAccount(null);
      openAuth();
      return false;
    }
    $("address-error").textContent = error.status === 400
      ? "Periksa lagi alamatnya. Nomor HP harus nomor ponsel Indonesia dan kode pos 5 angka."
      : "Alamat belum bisa disimpan. Coba lagi sebentar lagi.";
    $("address-error").hidden = false;
    return false;
  }
}

async function submitAddress(event) {
  event.preventDefault();
  $("address-save").disabled = true;
  await trySaveAddress();
  $("address-save").disabled = false;
}

async function submitCheckout() {
  if (checkoutBlocked) return;
  const editing = !$("address-form").hidden;
  if (editing && !$("address-form").reportValidity()) return;
  if (!shippingQuote()) {
    $("checkout-error").textContent = "Pilih kurir dulu.";
    $("checkout-error").hidden = false;
    return;
  }
  $("checkout-submit").disabled = true;
  $("checkout-error").hidden = true;
  try {
    if (editing && !(await trySaveAddress())) return;
    const order = { items: checkoutItems.map(({ variant_id, quantity }) => ({ variant_id, quantity })), shipping_address: savedAddress, shipping_option: chosenShipping };
    if (appliedVoucher) order.voucher_code = appliedVoucher.code;
    await post("/orders", order);
  } catch (error) {
    const message = orderError(error);
    if (error.status === 401) {
      openAuth();
      return;
    }
    // The voucher stopped being usable since it was applied, such as its quota running out.
    if (error.status === 422) {
      showVoucher(null);
      voucherProblem(`${message} Pesanan belum dibuat. Lanjutkan tanpa voucher atau pakai kode lain.`);
      return;
    }
    $("checkout-error").textContent = message;
    $("checkout-error").hidden = false;
    return;
  } finally {
    $("checkout-submit").disabled = checkoutBlocked;
  }
  $("checkout-dialog").close();
  openOrders();
  if (!checkoutFromCart) return;
  cart = [];
  showCartCount();
  try {
    await send("DELETE", "/cart");
  } catch {
    // The order exists. Its items only show again in the cart until they're removed.
  }
}

// Another tab or browser may have changed the account, or logged it out.
const accountCheckGap = 5000;
let lastAccountCheck = 0;

function checkAccountAgain() {
  if (document.visibilityState !== "visible" || $("cart-dialog").open) return;
  if (Date.now() - lastAccountCheck < accountCheckGap) return;
  lastAccountCheck = Date.now();
  loadAccount();
}

$("order-form").addEventListener("submit", submitCartAdd);
$("photo-zoom").addEventListener("click", openPhoto);
$("photo-image").addEventListener("load", fitPhoto);
$("photo-stage").addEventListener("pointerdown", photoPointerDown);
$("photo-stage").addEventListener("pointermove", photoPointerMove);
$("photo-stage").addEventListener("pointerup", photoPointerUp);
$("photo-stage").addEventListener("pointercancel", photoPointerUp);
$("photo-stage").addEventListener("wheel", photoWheel, { passive: false });
// On the document, because a button that turns disabled sends focus out of the viewer.
document.addEventListener("keydown", (event) => {
  if ($("photo-dialog").open && !event.ctrlKey && !event.metaKey && !event.altKey) photoKey(event);
});
$("photo-zoom-in").addEventListener("click", () => zoomAt(viewer.scale * 1.5));
$("photo-zoom-out").addEventListener("click", () => zoomAt(viewer.scale / 1.5));
$("photo-prev").addEventListener("click", () => viewPhoto(shownPhoto - 1));
$("photo-next").addEventListener("click", () => viewPhoto(shownPhoto + 1));
$("photo-dialog").addEventListener("close", () => {
  // The sheet keeps showing the photo the customer ended on.
  selectPhoto(shownPhoto);
  viewer.pointers.clear();
  $("photo-stage").classList.remove("dragging");
});
window.addEventListener("resize", () => {
  if ($("photo-dialog").open) fitPhoto();
});
$("order-now").addEventListener("click", orderNow);
$("cart-open").addEventListener("click", openCart);
$("cart-note-open").addEventListener("click", () => {
  $("product-dialog").close();
  openCart();
});
$("cart-checkout").addEventListener("click", checkout);
$("checkout-submit").addEventListener("click", submitCheckout);
$("voucher-form").addEventListener("submit", submitVoucher);
$("voucher-remove").addEventListener("click", () => {
  showVoucher(null);
  $("voucher-code").focus();
});
$("address-form").addEventListener("submit", submitAddress);
$("address-edit").addEventListener("click", () => {
  showAddress(true);
  $("address-recipient").focus();
});
$("address-cancel").addEventListener("click", () => showAddress(false));
$("address-province").addEventListener("change", renderShipping);
document.addEventListener("visibilitychange", checkAccountAgain);
// Keeps a guest's second tab showing the same cart.
window.addEventListener("storage", (event) => {
  if (loggedIn || (event.key !== cartKey && event.key !== null)) return;
  cart = loadCart();
  showCartCount();
  if ($("cart-dialog").open) refreshCart();
});
$("orders-dialog").addEventListener("close", () => {
  cancelQuestion = null;
  detailOrder = null;
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
$("forgot-open").addEventListener("click", openForgot);
$("forgot-form").addEventListener("submit", submitForgot);
$("forgot-back").addEventListener("click", () => {
  $("forgot-dialog").close();
  openAuth();
});
$("reset-form").addEventListener("submit", submitReset);
window.addEventListener("hashchange", openResetFromLink);
for (const button of document.querySelectorAll("[data-close]")) {
  button.addEventListener("click", () => button.closest("dialog").close());
}

addStepper($("order-quantity"));
showCartCount();
renderCategories();
loadAccount();
openResetFromLink();
loadFilterChoices();
// Category names appear on the cards, so the list loads after them.
loadCategories().then(() => loadProducts());
