"use strict";

const pageSize = 12;
const $ = (id) => document.getElementById(id);
const rupiah = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });

const state = { q: "", categoryId: "", size: "", color: "", inStock: false, page: 1, offset: 0, total: 0, categories: new Map(), categoryPhotos: {}, productPhotos: {}, ratings: null };
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

// priceParts shows what a customer pays for quantity pieces. While a sale runs it comes after
// the regular price, struck through. Screen readers don't announce the strike, so it's said.
function priceParts(product, quantity = 1) {
  const now = rupiah.format(product.price * quantity);
  if (!product.original_price) return [now];
  const was = el("s", "price-was");
  was.append(el("span", "visually-hidden", "Harga normal "), rupiah.format(product.original_price * quantity));
  const sale = el("span", "price-sale");
  sale.append(el("span", "visually-hidden", ", harga promo "), now);
  return [was, " ", sale];
}

// saleBadge says how much less a product costs during its sale, such as "Hemat 20%".
function saleBadge(product) {
  const percent = Math.floor((1 - product.price / product.original_price) * 100);
  return el("span", "badge sale", percent >= 1 ? `Hemat ${percent}%` : "Promo");
}

// How many more sell at a running Flash Sale's price, or null when none runs.
function flashLeft(product) {
  const sale = product.sale;
  if (!product.original_price || !sale || !sale.flash || !sale.quota) return null;
  return Math.max(sale.quota - sale.sold, 0);
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
  const badges = el("span", "card-badges");
  if (stock === 0) badges.append(el("span", "badge sold-out", "Stok habis"));
  else if (stock <= 5) badges.append(el("span", "badge", `Sisa ${stock}`));
  if (product.original_price) badges.append(saleBadge(product));
  if (badges.children.length > 0) art.append(badges);
  art.append(el("span", "card-view", "Lihat detail"));
  card.append(art);
  const category = state.categories.get(product.category_id);
  if (category) card.append(el("span", "card-category", category));
  card.append(el("span", "card-name", product.name));
  const sizes = [...new Set((product.variants || []).map((variant) => variant.size))];
  if (sizes.length > 0) card.append(el("span", "card-meta", `Ukuran ${sizes.join(", ")}`));
  const rating = el("span", "card-rating");
  rating.dataset.product = product.id;
  fillCardRating(rating);
  card.append(rating);
  const price = el("span", "card-price");
  price.append(...priceParts(product));
  card.append(price);
  const left = flashLeft(product);
  if (left !== null) {
    const meter = el("span", "flash-meter");
    meter.setAttribute("aria-hidden", "true");
    const fill = el("span");
    fill.style.width = `${Math.round((product.sale.sold / product.sale.quota) * 100)}%`;
    meter.append(fill);
    card.append(meter, el("span", "flash-left", `Tersisa ${left} dengan harga ini`));
  }
  card.addEventListener("click", () => openProduct(product.id));
  // A button can't hold another, so the heart sits beside the card in a wrapper.
  const wrap = el("div", "card-wrap");
  wrap.append(card, favButton(product));
  return wrap;
}

// Stars for a rating, rounded to the nearest whole star. Purely visual: the number is
// always written next to them.
const starPath = "M12 2.8l2.8 5.9 6.4.8-4.7 4.4 1.2 6.4L12 17.2l-5.7 3.1 1.2-6.4-4.7-4.4 6.4-.8z";

function starIcon(on, size = 14) {
  const svgNS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(svgNS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", size);
  svg.setAttribute("height", size);
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("class", on ? "star on" : "star");
  const path = document.createElementNS(svgNS, "path");
  path.setAttribute("d", starPath);
  svg.append(path);
  return svg;
}

function starRow(rating, size) {
  const row = el("span", "stars");
  const whole = Math.round(rating);
  for (let i = 1; i <= 5; i++) row.append(starIcon(i <= whole, size));
  return row;
}

const decimal = new Intl.NumberFormat("id-ID", { minimumFractionDigits: 1, maximumFractionDigits: 1 });
const counted = new Intl.NumberFormat("id-ID");

// "★ 4,8 (23) · 61 terjual" under a card's name, filled in once the ratings arrive.
function fillCardRating(node) {
  if (state.ratings === null) return;
  const rating = state.ratings[node.dataset.product];
  const parts = [];
  if (rating && rating.count > 0) {
    const score = el("span", "");
    score.append(starIcon(true), " ", el("strong", "", decimal.format(rating.average)), ` (${counted.format(rating.count)})`);
    parts.push(score);
  } else {
    parts.push(el("span", "muted", "Belum ada ulasan"));
  }
  if (rating && rating.sold > 0) parts.push(el("span", "", ` · ${counted.format(rating.sold)} terjual`));
  node.replaceChildren(...parts);
}

async function loadRatings() {
  try {
    ({ ratings: state.ratings } = await api("/ratings"));
  } catch {
    return;
  }
  for (const node of document.querySelectorAll(".card-rating")) fillCardRating(node);
  if (shownProductId !== null) showProductRating(shownProductId);
}

// Grey boxes while the first products load. A page that already shows products keeps them
// until the next ones arrive.
function showSkeletons() {
  if ($("products").querySelector(".card-wrap, .skeleton")) return;
  $("products").replaceChildren(...Array.from({ length: 8 }, () => el("div", "skeleton")));
}

function emptyProducts() {
  const box = el("div", "empty-products");
  box.setAttribute("role", "status");
  box.append(el("strong", "", "Produk yang Anda cari belum tersedia"), el("span", "", "Coba kata atau saringan lain."));
  return box;
}

const svgNS = "http://www.w3.org/2000/svg";

function chevron(path) {
  const svg = document.createElementNS(svgNS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.8");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  const line = document.createElementNS(svgNS, "path");
  line.setAttribute("d", path);
  svg.append(line);
  return svg;
}

// The search service reads at most this far into the results.
const maxOffset = 9900;

// Page numbers: the first, the last, and the current one with its neighbours. A gap of one
// page shows that page rather than "…". go(n) opens page n.
function pagerItems(page, pages, go) {
  const step = (label, path, target) => {
    const button = el("button", "pager-step");
    button.type = "button";
    button.disabled = target < 1 || target > pages;
    const text = el("span", "pager-step-label", label);
    if (path.startsWith("M15")) button.append(chevron(path), text);
    else button.append(text, chevron(path));
    button.addEventListener("click", () => go(target));
    return button;
  };
  const number = (n) => {
    const button = el("button", "pager-page", String(n));
    button.type = "button";
    button.setAttribute("aria-label", `Halaman ${n}`);
    if (n === page) button.setAttribute("aria-current", "page");
    button.addEventListener("click", () => go(n));
    return button;
  };
  const shown = [...new Set([1, page - 1, page, page + 1, pages])].filter((n) => n >= 1 && n <= pages).sort((a, b) => a - b);
  const items = [step("Sebelumnya", "M15 6l-6 6 6 6", page - 1)];
  shown.forEach((n, i) => {
    const before = shown[i - 1];
    if (before !== undefined && n - before === 2) items.push(number(n - 1));
    else if (before !== undefined && n - before > 2) {
      const gap = el("span", "pager-gap", "…");
      gap.setAttribute("aria-hidden", "true");
      items.push(gap);
    }
    items.push(number(n));
  });
  items.push(step("Berikutnya", "M9 6l6 6-6 6", page + 1));
  return items;
}

function renderPager() {
  const pages = Math.min(Math.ceil(state.total / pageSize), Math.floor(maxOffset / pageSize) + 1);
  $("pager").hidden = pages <= 1;
  if (pages <= 1) return;
  $("pager-pages").replaceChildren(...pagerItems(state.page, pages, (n) => loadProducts(n, true)));
  const last = Math.min(state.offset + pageSize, state.total);
  $("pager-range").textContent = `Menampilkan ${state.offset + 1}–${last} dari ${counted.format(state.total)} produk`;
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

// Loads one page of the catalog. A new search or filter starts again at page 1; a page number
// brings the catalog's top back into view.
async function loadProducts(page = 1, scroll = false) {
  const request = ++latestRequest;
  state.offset = (page - 1) * pageSize;
  setStatus("Memuat produk…");
  showSkeletons();
  try {
    const result = await api(`/search?${searchParams()}`);
    if (request !== latestRequest) return;
    // Products may have gone since the page numbers were drawn.
    if (result.products.length === 0 && result.total > 0 && page > 1) {
      loadProducts(1, scroll);
      return;
    }
    state.page = page;
    state.total = result.total;
    if (result.total === 0) {
      $("products").replaceChildren(emptyProducts());
      setStatus("");
    } else {
      $("products").replaceChildren(...result.products.map(productCard));
      setStatus(`${counted.format(result.total)} produk`);
    }
    renderPager();
    if (scroll) $("katalog").scrollIntoView();
  } catch {
    if (request !== latestRequest) return;
    $("products").replaceChildren();
    $("pager").hidden = true;
    setStatus("Produk belum bisa dimuat. Coba lagi sebentar lagi.", true);
  }
}

// Highlights: four short lists made from the catalog and its ratings. A list with nothing in
// it, such as "Sedang diskon" without a sale, has no tab, and with all four empty the section
// stays hidden. They come from the first 100 products, which is the whole catalog for now.
const pickKinds = [
  { id: "new", label: "Terbaru", note: "Koleksi yang paling baru masuk." },
  { id: "best", label: "Terlaris", note: "Paling banyak dibeli pembeli." },
  { id: "rated", label: "Ulasan teratas", note: "Rating tertinggi dari pembeli." },
  { id: "sale", label: "Sedang diskon", note: "Harga coret, selama promo masih berlaku." },
];
const pickMax = 10;
const pickEvery = 5000;
const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

let catalogSampleRequest = null;

function catalogSample() {
  catalogSampleRequest ??= api("/search?limit=100").then((result) => result.products);
  return catalogSampleRequest;
}

function pickLists(products, ratings) {
  const inStock = products.filter((product) => totalStock(product) > 0);
  const rating = (product) => ratings[product.id] || { average: 0, count: 0, sold: 0 };
  const off = (product) => 1 - product.price / product.original_price;
  const lists = {
    new: [...inStock].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)),
    best: inStock.filter((p) => rating(p).sold > 0).sort((a, b) => rating(b).sold - rating(a).sold),
    rated: inStock.filter((p) => rating(p).count > 0).sort((a, b) => rating(b).average - rating(a).average || rating(b).count - rating(a).count),
    sale: inStock.filter((p) => p.original_price > p.price).sort((a, b) => off(b) - off(a)),
  };
  for (const id of Object.keys(lists)) lists[id] = lists[id].slice(0, pickMax);
  return lists;
}

// A product card with a label saying why it's here. Discounts already show "Hemat x%".
function pickCard(product, kind, index) {
  const wrap = productCard(product);
  const art = wrap.querySelector(".card-art");
  let badges = art.querySelector(".card-badges");
  if (!badges) {
    badges = el("span", "card-badges");
    art.insertBefore(badges, art.querySelector(".card-view"));
  }
  if (kind === "new") badges.prepend(el("span", "badge", "Baru"));
  if (kind === "best") badges.prepend(el("span", "badge dark", `Terlaris #${index + 1}`));
  if (kind === "rated") badges.prepend(el("span", "badge", `★ ${decimal.format(state.ratings[product.id].average)}`));
  return wrap;
}

// One sliding row of highlights. Its parts are the elements with ids id, id-tabs, id-note,
// id-track, id-dots, id-prev and id-next, so the homepage and the account page each have one.
function createPicks(id) {
  const part = (name) => $(`${id}-${name}`);
  const section = $(id);
  const track = part("track");
  const self = { lists: {}, timer: null, hovered: false, focused: false, touchedAt: 0 };

  // The row moves by as many cards as fit, and always stops at a card's edge. Each dot is one
  // such step.
  const layout = () => {
    const cards = [...track.children];
    if (cards.length === 0) return { cards, perView: 1, pages: 1, step: 1 };
    const style = getComputedStyle(track);
    const width = cards[0].getBoundingClientRect().width;
    const gap = parseFloat(style.columnGap) || 0;
    const view = track.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
    const perView = Math.max(1, Math.floor((view + gap + 1) / (width + gap)));
    return { cards, perView, pages: Math.ceil(cards.length / perView), step: (width + gap) * perView };
  };
  const current = () => {
    const { pages, step } = layout();
    if (track.scrollLeft >= track.scrollWidth - track.clientWidth - 2) return pages - 1;
    return Math.min(pages - 1, Math.round(track.scrollLeft / step));
  };
  const scrollTo = (page) => {
    const { cards, perView, pages } = layout();
    if (cards.length === 0) return;
    const target = ((page % pages) + pages) % pages;
    const card = cards[target * perView];
    const left = card.getBoundingClientRect().left - track.getBoundingClientRect().left + track.scrollLeft - parseFloat(getComputedStyle(track).paddingLeft);
    track.scrollTo({ left: Math.min(left, track.scrollWidth - track.clientWidth), behavior: reducedMotion.matches ? "auto" : "smooth" });
  };
  const renderDots = () => {
    const { pages } = layout();
    const now = current();
    const dots = [];
    for (let i = 0; i < pages && pages > 1; i++) {
      const dot = el("button", "picks-dot");
      dot.type = "button";
      dot.setAttribute("aria-label", `Bagian ${i + 1} dari ${pages}`);
      if (i === now) dot.setAttribute("aria-current", "true");
      dot.addEventListener("click", () => scrollTo(i));
      dots.push(dot);
    }
    part("dots").replaceChildren(...dots);
  };
  const markDot = () => {
    const now = current();
    [...part("dots").children].forEach((dot, i) => {
      if (i === now) dot.setAttribute("aria-current", "true");
      else dot.removeAttribute("aria-current");
    });
  };
  const show = (kindId) => {
    for (const chip of part("tabs").children) chip.setAttribute("aria-pressed", String(chip.dataset.kind === kindId));
    part("note").textContent = pickKinds.find((kind) => kind.id === kindId).note;
    track.replaceChildren(...self.lists[kindId].map((product, index) => pickCard(product, kindId, index)));
    track.scrollLeft = 0;
    renderDots();
  };
  // Slides on by itself while it's on screen, but waits while someone points at it, works in
  // it, or just swiped it.
  const advance = () => {
    if (section.hidden || section.offsetParent === null || document.hidden || self.hovered || self.focused) return;
    if (Date.now() - self.touchedAt < 2 * pickEvery) return;
    scrollTo(current() + 1);
  };

  part("prev").addEventListener("click", () => scrollTo(current() - 1));
  part("next").addEventListener("click", () => scrollTo(current() + 1));
  let frame = 0;
  track.addEventListener("scroll", () => {
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(markDot);
  }, { passive: true });
  track.addEventListener("touchstart", () => { self.touchedAt = Date.now(); }, { passive: true });
  section.addEventListener("pointerenter", (event) => { if (event.pointerType === "mouse") self.hovered = true; });
  section.addEventListener("pointerleave", () => { self.hovered = false; });
  section.addEventListener("focusin", () => { self.focused = true; });
  section.addEventListener("focusout", (event) => { self.focused = section.contains(event.relatedTarget); });
  window.addEventListener("resize", () => { if (!section.hidden) renderDots(); });

  return {
    async load() {
      let products;
      try {
        products = await catalogSample();
      } catch {
        return;
      }
      self.lists = pickLists(products, state.ratings || {});
      const kinds = pickKinds.filter((kind) => self.lists[kind.id].length > 0);
      section.hidden = kinds.length === 0;
      if (kinds.length === 0) return;
      part("tabs").replaceChildren(...kinds.map((kind) => {
        const chip = el("button", "chip pick-chip", kind.label);
        chip.type = "button";
        chip.dataset.kind = kind.id;
        chip.addEventListener("click", () => show(kind.id));
        return chip;
      }));
      show(kinds[0].id);
      clearInterval(self.timer);
      if (!reducedMotion.matches) self.timer = setInterval(advance, pickEvery);
    },
    // A row drawn while hidden has no width, so its dots are counted again once it shows.
    refresh: renderDots,
  };
}

const homePicks = createPicks("picks");

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
  renderHero();
}

// The hero shows the dress, hijab and blouse photos. Without all three, the drawings stay.
// The Flash Sale section: products whose Flash Sale runs now, and a countdown to the first
// one to end. When it ends the list is read again, since prices have changed.
let flashTimer = null;

async function loadFlash() {
  clearInterval(flashTimer);
  let products;
  try {
    ({ products } = await api("/flash-sale"));
  } catch {
    products = [];
  }
  $("flash").hidden = products.length === 0;
  $("nav-flash").hidden = products.length === 0;
  if (products.length === 0) return;
  $("flash-products").replaceChildren(...products.map(productCard));
  const end = Math.min(...products.map((product) => Date.parse(product.sale.ends_at)));
  const pad = (n) => String(n).padStart(2, "0");
  const tick = () => {
    const left = Math.max(0, Math.floor((end - Date.now()) / 1000));
    $("flash-hh").textContent = pad(Math.floor(left / 3600));
    $("flash-mm").textContent = pad(Math.floor((left % 3600) / 60));
    $("flash-ss").textContent = pad(left % 60);
    if (left === 0) setTimeout(loadFlash, 10000);
    return left;
  };
  if (tick() > 0) flashTimer = setInterval(() => { if (tick() === 0) clearInterval(flashTimer); }, 1000);
}

// Shop the look: the admins' photo of an outfit, with a point on each product worn. Products
// no longer sold are left out, and the section stays hidden without any.
async function loadLook() {
  let look;
  try {
    ({ look } = await api("/look"));
  } catch {
    return;
  }
  if (!look) return;
  const results = await Promise.allSettled(look.items.map((item) => api(`/products/${item.product_id}`)));
  const shown = look.items
    .map((item, i) => ({ ...item, product: results[i].status === "fulfilled" ? results[i].value : null }))
    .filter((item) => item.product);
  if (shown.length === 0) return;
  $("look-eyebrow").textContent = look.eyebrow;
  $("look-eyebrow").hidden = !look.eyebrow;
  $("look-title").replaceChildren(look.title);
  if (look.accent) $("look-title").append(", ", el("em", "", look.accent));
  $("look-img").src = look.photo_url;
  $("look-img").alt = [look.title, look.accent].filter(Boolean).join(", ");
  const dots = shown.map((item) => {
    const dot = el("button", "look-dot");
    dot.type = "button";
    dot.style.left = `${item.x}%`;
    dot.style.top = `${item.y}%`;
    dot.setAttribute("aria-label", `Lihat ${item.product.name}`);
    dot.append(el("span"));
    return dot;
  });
  const rows = shown.map(({ product }) => lookRow(product));
  const choose = (index) => {
    dots.forEach((dot, i) => dot.setAttribute("aria-pressed", String(i === index)));
    rows.forEach((row, i) => row.classList.toggle("chosen", i === index));
  };
  dots.forEach((dot, i) => dot.addEventListener("click", () => {
    choose(i);
    if (window.matchMedia("(max-width: 820px)").matches) rows[i].scrollIntoView({ block: "nearest", behavior: "smooth" });
  }));
  choose(0);
  $("look-photo").querySelectorAll(".look-dot").forEach((dot) => dot.remove());
  $("look-photo").append(...dots);
  $("look-items").replaceChildren(...rows);
  $("look").hidden = false;
}

// A product in the look. With one size and colour in stock it goes straight to the cart;
// otherwise its page opens to choose.
function lookRow(product) {
  const row = el("div", "look-row");
  const thumb = el("span");
  fillSwatch(thumb, product, "look-thumb");
  thumb.setAttribute("aria-hidden", "true");
  const name = el("button", "look-name");
  name.type = "button";
  const price = el("span", "card-price");
  price.append(...priceParts(product));
  name.append(el("strong", "", product.name), price);
  name.addEventListener("click", () => openProduct(product.id));
  const inStock = (product.variants || []).filter((variant) => variant.stock > 0);
  const add = el("button", "look-add", inStock.length === 0 ? "Stok habis" : "+ Keranjang");
  add.type = "button";
  add.disabled = inStock.length === 0;
  add.setAttribute("aria-label", `Tambah ${product.name} ke keranjang`);
  add.addEventListener("click", async () => {
    if (inStock.length !== 1) {
      openProduct(product.id);
      return;
    }
    add.disabled = true;
    try {
      const skipped = await addToCart([{ product_id: product.id, variant_id: inStock[0].id, quantity: 1 }]);
      add.textContent = skipped > 0 ? "Keranjang penuh" : "Masuk ✓";
    } catch (error) {
      add.textContent = "Coba lagi";
      setStatus(cartWriteMessage(error), true);
    }
    setTimeout(() => {
      add.textContent = "+ Keranjang";
      add.disabled = false;
    }, 2000);
  });
  row.append(thumb, name, add);
  return row;
}

// The voucher the admins put in the bar above the header, if one can be used now.
async function loadPromo() {
  let voucher;
  try {
    ({ voucher } = await api("/vouchers/banner"));
  } catch {
    return;
  }
  if (!voucher) return;
  let terms = voucher.description || voucherSummary(voucher);
  if (!voucher.description && voucher.min_subtotal > 0) terms += ` · min. belanja ${rupiah.format(voucher.min_subtotal)}`;
  $("promo-text").replaceChildren("Pakai kode ", el("strong", "", voucher.code), ` · ${terms}`);
  const copy = $("promo-copy");
  let reset;
  copy.setAttribute("aria-label", `Salin kode ${voucher.code}`);
  copy.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(voucher.code);
      copy.textContent = "Tersalin ✓";
    } catch {
      copy.textContent = `Kode: ${voucher.code}`;
    }
    clearTimeout(reset);
    reset = setTimeout(() => { copy.textContent = "Salin kode"; }, 2000);
  });
  $("promo-bar").hidden = false;
}

function renderHero() {
  const urls = ["Dress", "Hijab", "Blouse"].map(categoryPhotoURL);
  if (urls.some((url) => !url)) return;
  const images = urls.map((url, index) => {
    const image = el("img", `hero-photo hero-photo-${index}`);
    image.src = url;
    image.alt = "";
    image.decoding = "async";
    return image;
  });
  let loaded = 0;
  for (const image of images) {
    image.addEventListener("load", () => {
      loaded += 1;
      if (loaded === images.length) {
        $("hero-art").replaceChildren(...images);
        $("hero-art").classList.add("has-photos");
      }
    });
  }
}

// Picking a category filters the catalog. Picking it again shows every category.
function chooseCategory(id) {
  state.categoryId = id;
  markCategories();
  loadProducts();
}

// Marks the chosen category on the chips and tiles in place, so a tile can rise smoothly.
function markCategories() {
  for (const button of document.querySelectorAll("#categories .chip, #category-tiles .tile")) {
    button.setAttribute("aria-pressed", String(button.dataset.id === state.categoryId));
  }
}

function renderCategories() {
  const chip = (label, id) => {
    const button = el("button", "chip", label);
    button.type = "button";
    button.dataset.id = id;
    button.addEventListener("click", () => chooseCategory(id));
    return button;
  };
  const tile = (id, name, index) => {
    const button = el("button", "tile");
    button.type = "button";
    button.dataset.id = String(id);
    const art = el("span", `tile-art tone-${index % 5}`);
    art.setAttribute("aria-hidden", "true");
    fillArt(art, name, name);
    if (addCategoryPhoto(art, name, "tile-photo", () => button.classList.remove("has-photo"))) {
      button.classList.add("has-photo");
    }
    button.append(art, el("span", "tile-name", name));
    button.addEventListener("click", () => {
      const again = state.categoryId === String(id);
      chooseCategory(again ? "" : String(id));
      if (!again) $("katalog").scrollIntoView();
    });
    return button;
  };
  const categories = [...state.categories];
  $("categories").replaceChildren(chip("Semua", ""), ...categories.map(([id, name]) => chip(name, String(id))));
  $("category-tiles").replaceChildren(...categories.map(([id, name], index) => tile(id, name, index)));
  $("shop-by").hidden = categories.length === 0;
  markCategories();
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
    const products = await catalogSample();
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
    $("product-price").replaceChildren(...priceParts(product));
    if (product.original_price && product.sale) {
      const left = flashLeft(product);
      const until = `sampai ${dateTime.format(new Date(product.sale.ends_at))}`;
      $("product-price").append(el("span", "price-until",
        left === null ? `Harga promo ${until}` : `Flash Sale ${until} · tersisa ${left} dengan harga ini`));
    }
    $("product-description").textContent = product.description;
    $("product-variants").replaceChildren(...(product.variants || []).map(variantChoice));
    $("order-quantity").value = 1;
    $("order-error").hidden = true;
    $("cart-note").hidden = true;
    shownProductId = product.id;
    chatSawProduct(product);
    showProductRating(product.id);
    loadReviews(product.id, 0);
    markFavorites();
    const first = document.querySelector('input[name="variant"]:not(:disabled)');
    if (first) first.checked = true;
    $("cart-add").disabled = !first;
    $("order-now").disabled = !first;
    $("product-dialog").showModal();
  } catch (error) {
    setStatus(error.status === 404 ? "Produk ini sudah tidak tersedia." : "Produk belum bisa dibuka. Coba lagi sebentar lagi.", true);
  }
}

// The rating under the product's name, which jumps to its reviews.
function showProductRating(id) {
  const rating = state.ratings && state.ratings[id];
  const link = $("product-rating");
  link.hidden = !rating || rating.count === 0;
  if (link.hidden) return;
  const parts = [starRow(rating.average, 16), el("strong", "", decimal.format(rating.average)), el("span", "rating-count", `${counted.format(rating.count)} ulasan`)];
  if (rating.sold > 0) parts.push(el("span", "", `· ${counted.format(rating.sold)} terjual`));
  link.replaceChildren(...parts);
}

// The product's reviews: a summary with a bar per number of stars, which filter the list like
// the chips above it, and the newest reviews a page at a time.
const reviewPage = 5;
const reviews = { productId: null, stars: 0, offset: 0, request: 0 };
const reviewDate = new Intl.DateTimeFormat("id-ID", { day: "numeric", month: "short", year: "numeric" });

async function loadReviews(productId, stars, more = false) {
  const request = ++reviews.request;
  if (!more) {
    Object.assign(reviews, { productId, stars, offset: 0 });
    $("review-list").replaceChildren();
  }
  $("review-more").hidden = true;
  $("review-status").textContent = "Memuat ulasan…";
  const params = new URLSearchParams({ limit: reviewPage, offset: reviews.offset });
  if (stars) params.set("rating", stars);
  let page;
  try {
    page = await api(`/products/${productId}/reviews?${params}`);
  } catch {
    if (request === reviews.request) $("review-status").textContent = "Ulasan belum bisa dimuat. Coba lagi sebentar lagi.";
    return;
  }
  if (request !== reviews.request) return;
  showReviewSummary(page);
  $("review-list").append(...page.reviews.map(reviewItem));
  reviews.offset += page.reviews.length;
  $("review-more").hidden = reviews.offset >= page.matching;
  $("review-status").textContent = page.count === 0
    ? "Belum ada ulasan. Ulasan muncul dari pembeli yang pesanannya sudah selesai."
    : page.matching === 0 ? "Belum ada ulasan dengan bintang ini." : "";
}

function showReviewSummary(page) {
  $("review-summary").hidden = page.count === 0;
  $("review-chips").hidden = page.count === 0;
  if (page.count === 0) return;
  $("review-average").textContent = decimal.format(page.average);
  $("review-stars").replaceChildren(starRow(page.average, 20));
  $("review-total").textContent = `${counted.format(page.count)} ulasan dari pembeli yang pesanannya sudah selesai`;
  const most = Math.max(...page.counts);
  $("review-bars").replaceChildren(...[5, 4, 3, 2, 1].map((stars) => {
    const bar = el("button", "review-bar");
    bar.type = "button";
    bar.setAttribute("aria-pressed", String(reviews.stars === stars));
    bar.setAttribute("aria-label", `${stars} bintang: ${page.counts[stars - 1]} ulasan`);
    const track = el("span", "review-track");
    const fill = el("span");
    fill.style.width = most > 0 ? `${(page.counts[stars - 1] / most) * 100}%` : "0";
    track.append(fill);
    bar.append(el("span", "", `${stars}★`), track, el("span", "review-bar-count", counted.format(page.counts[stars - 1])));
    bar.addEventListener("click", () => loadReviews(reviews.productId, reviews.stars === stars ? 0 : stars));
    return bar;
  }));
  $("review-chips").replaceChildren(...[0, 5, 4, 3, 2, 1].map((stars) => {
    const chip = el("button", "chip", stars === 0 ? "Semua" : `${stars}★`);
    chip.type = "button";
    chip.setAttribute("aria-pressed", String(reviews.stars === stars));
    if (stars) chip.setAttribute("aria-label", `${stars} bintang`);
    chip.addEventListener("click", () => loadReviews(reviews.productId, stars));
    return chip;
  }));
}

function reviewItem(review) {
  const item = el("article", "review");
  const head = el("div", "review-head");
  const who = el("div", "review-who");
  who.append(el("span", "review-initial", Array.from(review.author)[0] || "P"));
  const name = el("div", "");
  const meta = el("div", "review-meta");
  if (review.variant) meta.append(`${review.variant} · `);
  meta.append(el("span", "review-verified", "✓ Pembeli terverifikasi"));
  name.append(el("div", "review-name", review.author), meta);
  who.append(name);
  head.append(who, el("span", "review-date", reviewDate.format(new Date(review.created_at))));
  const stars = starRow(review.rating, 16);
  stars.setAttribute("role", "img");
  stars.setAttribute("aria-label", `${review.rating} dari 5 bintang`);
  item.append(head, stars);
  if (review.body) item.append(el("p", "review-body", review.body));
  return item;
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
// Whether /me has answered, so the account page knows to ask for a login.
let accountKnown = false;

function setAuthMode(mode) {
  authMode = mode;
  const registering = mode === "register";
  $("auth-title").textContent = registering ? "Daftar" : "Masuk";
  $("auth-submit").textContent = registering ? "Daftar" : "Masuk";
  $("auth-switch-text").textContent = registering ? "Sudah punya akun?" : "Belum punya akun?";
  $("google-text").textContent = registering ? "Daftar dengan Google" : "Masuk dengan Google";
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

// The email waiting for its code, after signing up or logging in before verifying.
let verifyEmail = "";
// When "Kirim ulang" may be pressed again, so a tap doesn't send a burst of codes.
const resendWait = 60;

function holdResend(button) {
  let left = resendWait;
  button.disabled = true;
  const label = button.dataset.label || (button.dataset.label = button.textContent);
  button.textContent = `${label} (${left})`;
  const timer = setInterval(() => {
    left -= 1;
    if (left > 0 && button.isConnected) {
      button.textContent = `${label} (${left})`;
      return;
    }
    clearInterval(timer);
    button.textContent = label;
    button.disabled = false;
  }, 1000);
}

function openVerify(email, note = "") {
  verifyEmail = email;
  $("auth-dialog").close();
  $("verify-form").reset();
  $("verify-text").textContent = `Kami mengirim 6 angka ke ${email}. Masukkan di sini untuk memastikan email itu milikmu. Cek juga folder spam.`;
  $("verify-note").textContent = note;
  $("verify-note").hidden = !note;
  $("verify-error").hidden = true;
  holdResend($("verify-resend"));
  $("verify-dialog").showModal();
  $("verify-code").focus();
}

function codeMessage(error) {
  switch (error.status) {
    case 400:
    case 422: return "Kodenya salah atau sudah tidak berlaku. Periksa lagi, atau minta kode baru.";
    case 429: return "Terlalu banyak kode diminta. Coba lagi nanti.";
    default: return "Layanan belum tersedia. Coba lagi sebentar lagi.";
  }
}

async function submitVerify(event) {
  event.preventDefault();
  const code = $("verify-code").value.trim();
  $("verify-error").hidden = true;
  $("verify-note").hidden = true;
  $("verify-submit").disabled = true;
  try {
    renderAccount(await post("/email/verify", { email: verifyEmail, code }));
    $("verify-dialog").close();
  } catch (error) {
    $("verify-error").textContent = codeMessage(error);
    $("verify-error").hidden = false;
  } finally {
    $("verify-submit").disabled = false;
  }
}

async function resendVerify() {
  $("verify-error").hidden = true;
  holdResend($("verify-resend"));
  try {
    await post("/email/code", { email: verifyEmail });
    $("verify-note").textContent = "Kode baru sudah dikirim. Kode yang lama tidak berlaku lagi.";
    $("verify-note").hidden = false;
  } catch (error) {
    $("verify-error").textContent = codeMessage(error);
    $("verify-error").hidden = false;
  }
}

// Google is offered only when the shop has it set up.
async function loadAuthOptions() {
  try {
    const options = await api("/auth/options");
    $("google-login").hidden = !options.google;
  } catch {
    $("google-login").hidden = true;
  }
}

// web sends a failed Google login back here, with #login=google-failed.
function openGoogleFailure() {
  if (location.hash !== "#login=google-failed") return;
  history.replaceState(null, "", location.pathname + location.search);
  openAuth();
  $("auth-error").textContent = "Masuk dengan Google belum berhasil. Coba lagi, atau masuk dengan email dan password.";
  $("auth-error").hidden = false;
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
      // The account can log in once the code from the email comes back.
      openVerify(email);
      return;
    }
    renderAccount(await post("/login", { email, password }));
    $("auth-form").reset();
    $("auth-dialog").close();
  } catch (error) {
    // The password was right, and user-service just sent a code to the email.
    if (error.status === 403) {
      openVerify(email, "Email ini belum diverifikasi. Kami baru saja mengirim kodenya.");
      return;
    }
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
    if ($("fav-dialog").open) refreshFavorites();
    else syncAccountFavorites();
  } else if (wasLoggedIn) {
    favorites = loadGuestFavorites();
    markFavorites();
    if ($("fav-dialog").open) refreshFavorites();
    cart = loadCart();
    showCartCount();
    // Only an ended session gets here with the cart open, because logging out is behind it.
    if ($("cart-dialog").open) {
      refreshCart("Silakan masuk lagi untuk melihat keranjang akunmu.");
      openAuth();
    }
  }
  accountKnown = true;
  // account.js runs after this file. Should the answer come first, it draws these itself.
  if (typeof accountChanged === "function") accountChanged(user);
  if (typeof chatUserChanged === "function") chatUserChanged(user);
  const account = $("account");
  if (!user) {
    const login = el("button", "secondary", "Masuk");
    login.type = "button";
    login.addEventListener("click", () => openAuth());
    account.replaceChildren(login);
    return;
  }
  // The menu under the name, and the account page, are in account.js.
  if (typeof accountMenu === "function") account.replaceChildren(accountMenu(user));
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
  arrived: ["Sudah sampai", "arrived"],
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
const openStatuses = new Set(["pending", "awaiting_payment", "paying", "paid", "packed", "shipped", "arrived"]);
let pollTimer = null;
let shownOrders = [];
let liveSocket = null;
let liveTimer = null;
let liveRetry = 1000;

const isLive = () => liveSocket !== null && liveSocket.readyState === WebSocket.OPEN;

function orderState(order) {
  if (order.status === "rejected" && order.reason === "sale_sold_out") return ["Ditolak: kuota Flash Sale habis", "bad"];
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
  if (canBuyAgain(order)) target.append(buyAgainButton(order, "secondary small", (text) => { $("orders-status").textContent = text; }, () => $("orders-dialog").close()));
  if (order.status === "arrived") target.append(receiveControls(order));
  if (order.status === "completed") target.append(reviewControls(order));
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

const longDate = new Intl.DateTimeFormat("id-ID", { weekday: "long", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

// An arrived order waits for the customer to say it reached them, or completes by itself.
function receiveControls(order) {
  const box = el("div", "order-receive");
  const note = el("p", "receive-note");
  note.append("Barang sudah sampai pada ", el("strong", "", longDate.format(new Date(order.arrived_at))),
    ". Cek barangnya, lalu tekan ", el("strong", "", "Pesanan diterima"), ".");
  if (order.auto_complete_at) note.append(" Jika tidak, pesanan otomatis selesai pada ", el("strong", "", longDate.format(new Date(order.auto_complete_at))), ".");
  const receive = el("button", "primary", "Pesanan diterima");
  receive.type = "button";
  receive.dataset.key = `receive-${order.id}`;
  receive.addEventListener("click", async () => {
    receive.disabled = true;
    try {
      await post(`/orders/${order.id}/receive`, {});
      $("orders-status").textContent = "Terima kasih! Pesanan selesai. Beri ulasan untuk produknya di bawah.";
    } catch (error) {
      if (error.status === 401) renderAccount(null);
      $("orders-status").textContent = error.status === 409 ? "Pesanan ini sudah selesai." : "Pesanan belum bisa dikonfirmasi. Coba lagi sebentar lagi.";
    }
    await refreshOrders();
  });
  box.append(note, receive);
  return box;
}

// A review form stays open, with what was typed, while the list is drawn again.
const reviewWords = ["", "Sangat kecewa", "Kurang", "Cukup", "Bagus", "Sangat bagus"];
const reviewDrafts = new Map();
let openReview = null;

// One row per product in a completed order, to review it or change the review.
function reviewControls(order) {
  const box = el("div", "order-reviews");
  const seen = new Set();
  for (const item of order.items) {
    if (seen.has(item.product_id)) continue;
    seen.add(item.product_id);
    const variants = order.items.filter((other) => other.product_id === item.product_id).map((other) => `${other.size} · ${other.color} × ${other.quantity}`);
    box.append(reviewRow(order, item, variants.join(", ")));
  }
  return box;
}

function reviewRow(order, item, variants) {
  const key = `${order.id}:${item.product_id}`;
  const mine = order.reviews && order.reviews[item.product_id];
  const row = el("div", "review-row");
  const line = el("div", "review-product");
  const thumb = el("span", "review-thumb");
  thumb.setAttribute("aria-hidden", "true");
  const photo = (state.productPhotos[item.product_id] || [])[0];
  if (photo) addPhoto(thumb, photo, "");
  const text = el("div", "review-product-text");
  text.append(el("span", "review-product-name", item.product_name), el("span", "review-product-meta", variants));
  line.append(thumb, text);
  row.append(line);
  const open = () => {
    openReview = key;
    if (!reviewDrafts.has(key)) reviewDrafts.set(key, { stars: mine ? mine.rating : 0, body: mine ? mine.body : "" });
    renderOrders(shownOrders);
    const form = $("orders-list").querySelector(`[data-key="stars-${key}"]`);
    if (form) form.focus();
  };
  if (openReview === key) {
    row.append(reviewForm(order, item, key));
  } else if (mine) {
    const done = el("div", "review-done");
    const sent = el("span", "review-sent");
    const stars = starRow(mine.rating, 14);
    stars.setAttribute("role", "img");
    stars.setAttribute("aria-label", `Ulasanmu: ${mine.rating} bintang`);
    sent.append(stars, "Ulasan terkirim");
    done.append(sent, linkButton("Ubah", `review-${key}`, open));
    done.lastChild.setAttribute("aria-label", `Ubah ulasan ${item.product_name}`);
    row.append(done);
  } else {
    const give = el("button", "primary review-give", "Beri ulasan");
    give.type = "button";
    give.dataset.key = `review-${key}`;
    give.setAttribute("aria-label", `Beri ulasan ${item.product_name}`);
    give.addEventListener("click", open);
    line.append(give);
  }
  return row;
}

function reviewForm(order, item, key) {
  const draft = reviewDrafts.get(key) || { stars: 0, body: "" };
  reviewDrafts.set(key, draft);
  const form = el("form", "review-form");
  form.noValidate = true;
  form.append(el("p", "review-ask", "Seberapa puas kamu?"));
  const group = el("div", "review-pick");
  group.setAttribute("role", "radiogroup");
  group.setAttribute("aria-label", `Bintang untuk ${item.product_name}`);
  const word = el("p", "review-word", draft.stars ? reviewWords[draft.stars] : "Pilih 1 sampai 5 bintang");
  const submit = el("button", "primary", "Kirim ulasan");
  submit.type = "submit";
  const buttons = [1, 2, 3, 4, 5].map((n) => {
    const star = el("button", "review-star");
    star.type = "button";
    star.setAttribute("role", "radio");
    star.setAttribute("aria-label", `${n} bintang, ${reviewWords[n]}`);
    star.append(starIcon(n <= draft.stars, 32));
    star.addEventListener("click", () => pick(n));
    return star;
  });
  const pick = (n) => {
    draft.stars = n;
    buttons.forEach((star, i) => {
      star.setAttribute("aria-checked", String(i + 1 === n));
      star.tabIndex = i + 1 === (n || 1) ? 0 : -1;
      star.firstChild.setAttribute("class", i < n ? "star on" : "star");
    });
    word.textContent = n ? reviewWords[n] : "Pilih 1 sampai 5 bintang";
    submit.setAttribute("aria-disabled", String(!n));
  };
  // Arrow keys move between the stars, as in any radio group.
  group.addEventListener("keydown", (event) => {
    const step = { ArrowRight: 1, ArrowUp: 1, ArrowLeft: -1, ArrowDown: -1 }[event.key];
    if (!step) return;
    event.preventDefault();
    const next = Math.min(5, Math.max(1, (draft.stars || 0) + step));
    pick(next);
    buttons[next - 1].focus();
  });
  buttons[0].dataset.key = `stars-${key}`;
  group.append(...buttons);
  pick(draft.stars);
  const id = `review-text-${order.id}-${item.product_id}`;
  const label = el("label", "review-label", "Ceritakan pengalamanmu (opsional)");
  label.htmlFor = id;
  const textarea = el("textarea");
  textarea.id = id;
  textarea.rows = 4;
  textarea.maxLength = 1000;
  textarea.placeholder = "Bahan, ukuran, warna, dan pengirimannya…";
  textarea.value = draft.body;
  textarea.dataset.key = `text-${key}`;
  textarea.addEventListener("input", () => { draft.body = textarea.value; });
  const error = el("p", "error", "");
  error.setAttribute("role", "alert");
  error.hidden = true;
  const actions = el("div", "review-actions");
  actions.append(submit, linkButton("Batal", `cancel-review-${key}`, () => {
    openReview = null;
    reviewDrafts.delete(key);
    renderOrders(shownOrders);
  }));
  form.append(group, word, label, textarea, el("p", "review-hint", "Namamu tampil disamarkan, seperti S***i."), error, actions);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!draft.stars) {
      error.textContent = "Pilih bintangnya dulu.";
      error.hidden = false;
      buttons[0].focus();
      return;
    }
    error.hidden = true;
    for (const control of form.querySelectorAll("button, textarea")) control.disabled = true;
    try {
      const saved = await send("PUT", `/orders/${order.id}/reviews/${item.product_id}`, { rating: draft.stars, body: draft.body.trim() });
      openReview = null;
      reviewDrafts.delete(key);
      shownOrders = shownOrders.map((shown) => (shown.id === order.id
        ? { ...shown, reviews: { ...(shown.reviews || {}), [item.product_id]: saved } }
        : shown));
      $("orders-status").textContent = "Ulasan terkirim. Terima kasih!";
      renderOrders(shownOrders);
      const again = $("orders-list").querySelector(`[data-key="review-${key}"]`);
      if (again) again.focus();
    } catch (failure) {
      if (failure.status === 401) {
        renderAccount(null);
        return;
      }
      error.textContent = failure.status === 409 ? "Pesanan ini belum selesai, jadi belum bisa diulas."
        : failure.status === 400 ? "Ulasan paling panjang 1000 huruf." : "Ulasan belum bisa dikirim. Coba lagi sebentar lagi.";
      error.hidden = false;
      for (const control of form.querySelectorAll("button, textarea")) control.disabled = false;
    }
  });
  return form;
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
  if (order.arrived_at) fact("Sampai", when(order.arrived_at));
  if (order.completed_at) {
    const by = { customer: " · dikonfirmasi olehmu", system: " · otomatis oleh sistem" }[order.completed_by] || "";
    fact("Selesai", when(order.completed_at) + by);
  }
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
  const ask = el("button", "ask-chat", "Tanya soal pesanan ini");
  ask.type = "button";
  ask.dataset.key = `ask-${order.id}`;
  ask.prepend(chatIcon());
  ask.addEventListener("click", () => askAbout(orderAttachment(order)));
  box.append(ask);
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
      // The same connection also tells of chat messages, which chat.js handles.
      const signal = JSON.parse(event.data);
      if (signal.order_id) updateOrders([signal.order_id]);
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
// Favorites: product ids, newest first. A guest's stay in this browser and move to the account
// when they log in, like the cart.
const favKey = "prastika-favorites";
const favMax = 100;
const heartPath = "M12 20s-7.5-4.6-9.2-9.4C1.6 7.2 3.8 4 7.2 4c2 0 3.6 1.1 4.8 2.8C13.2 5.1 14.8 4 16.8 4c3.4 0 5.6 3.2 4.4 6.6C19.5 15.4 12 20 12 20z";
let favorites = loadGuestFavorites();
let favSync = null;

function loadGuestFavorites() {
  try {
    const saved = JSON.parse(localStorage.getItem(favKey));
    if (!Array.isArray(saved)) return [];
    return [...new Set(saved.filter((id) => Number.isSafeInteger(id) && id > 0))].slice(0, favMax);
  } catch {
    return [];
  }
}

function saveGuestFavorites(ids) {
  try {
    if (ids.length === 0) localStorage.removeItem(favKey);
    else localStorage.setItem(favKey, JSON.stringify(ids));
  } catch {
    // Without storage the favorites still work until the page closes.
  }
}

function heartIcon() {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.8");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", heartPath);
  svg.append(path);
  return svg;
}

function favButton(product) {
  const button = el("button", "fav-toggle");
  button.type = "button";
  button.dataset.id = product.id;
  button.dataset.name = product.name;
  button.append(heartIcon());
  markFavorite(button);
  button.addEventListener("click", () => toggleFavorite(product.id));
  return button;
}

function markFavorite(button) {
  const on = favorites.includes(Number(button.dataset.id));
  button.setAttribute("aria-pressed", String(on));
  button.setAttribute("aria-label", `${on ? "Hapus dari favorit" : "Simpan ke favorit"}: ${button.dataset.name}`);
}

// Shows the favorites everywhere they appear: the count, the hearts and the product page.
function markFavorites() {
  const count = favorites.length;
  $("fav-count").textContent = count > 99 ? "99+" : count;
  $("fav-count").hidden = count === 0;
  $("fav-open").setAttribute("aria-label", `Favorit, ${count} produk`);
  for (const button of document.querySelectorAll(".fav-toggle")) markFavorite(button);
  const on = shownProductId !== null && favorites.includes(shownProductId);
  $("product-fav").setAttribute("aria-pressed", String(on));
  $("product-fav").querySelector("span").textContent = on ? "Tersimpan di favorit" : "Simpan ke favorit";
}

// Reads the account's favorites, first moving a guest's into it. Requests share one run.
function syncAccountFavorites() {
  favSync ||= (async () => {
    const guest = loadGuestFavorites();
    try {
      const result = guest.length > 0 ? await post("/favorites/merge", { product_ids: guest }) : await api("/favorites");
      if (guest.length > 0) saveGuestFavorites([]);
      if (!loggedIn) return false;
      favorites = result.product_ids;
      markFavorites();
      return true;
    } catch (error) {
      if (error.status === 401) renderAccount(null);
      return false;
    }
  })().finally(() => { favSync = null; });
  return favSync;
}

// Changes the heart at once, and puts it back if the account couldn't save it.
async function toggleFavorite(id) {
  const before = favorites;
  const adding = !favorites.includes(id);
  if (adding && favorites.length >= favMax) {
    setStatus(`Favorit sudah penuh (${favMax} produk). Hapus beberapa dulu.`, true);
    return;
  }
  favorites = adding ? [id, ...favorites] : favorites.filter((item) => item !== id);
  markFavorites();
  if (!loggedIn) {
    saveGuestFavorites(favorites);
    if ($("fav-dialog").open && !adding) renderFavorites();
    return;
  }
  try {
    const result = await send(adding ? "PUT" : "DELETE", `/favorites/${id}`, adding ? {} : undefined);
    favorites = result.product_ids;
  } catch (error) {
    favorites = before;
    if (error.status === 401) renderAccount(null);
    else setStatus("Favorit belum bisa disimpan. Coba lagi sebentar lagi.", true);
  }
  markFavorites();
  if ($("fav-dialog").open && !adding) renderFavorites();
}

let favDetails = new Map();
let latestFavRequest = 0;

function renderFavorites(note = "") {
  const shown = favorites.filter((id) => favDetails.has(id));
  $("fav-list").replaceChildren(...shown.map((id) => productCard(favDetails.get(id))));
  $("fav-status").textContent = note || (shown.length === 0 ? "Belum ada favorit. Ketuk ikon hati di produk untuk menyimpannya di sini." : "");
}

async function refreshFavorites() {
  const request = ++latestFavRequest;
  $("fav-list").replaceChildren();
  $("fav-status").textContent = "Memuat favorit…";
  if (loggedIn && !(await syncAccountFavorites())) {
    if (request === latestFavRequest) $("fav-status").textContent = "Favorit belum bisa dimuat. Tutup lalu buka lagi sebentar lagi.";
    return;
  }
  const ids = [...favorites];
  const results = await Promise.allSettled(ids.map((id) => api(`/products/${id}`)));
  if (request !== latestFavRequest) return;
  if (results.some((result) => result.status === "rejected" && result.reason.status !== 404)) {
    $("fav-status").textContent = "Favorit belum bisa dimuat. Tutup lalu buka lagi sebentar lagi.";
    return;
  }
  favDetails = new Map();
  results.forEach((result, i) => {
    if (result.status === "fulfilled") favDetails.set(ids[i], result.value);
  });
  // Products no longer sold leave the list.
  const gone = ids.filter((id) => !favDetails.has(id));
  if (gone.length > 0) {
    favorites = favorites.filter((id) => !gone.includes(id));
    markFavorites();
    if (loggedIn) Promise.allSettled(gone.map((id) => send("DELETE", `/favorites/${id}`)));
    else saveGuestFavorites(favorites);
  }
  renderFavorites(gone.length > 0 ? `${gone.length} produk dihapus karena sudah tidak dijual.` : "");
}

function openFavorites() {
  $("fav-dialog").showModal();
  refreshFavorites();
}

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
  about.append(el("div", "cart-name", product.name), el("div", "cart-detail", `${variant.size} · ${variant.color}`));
  const unitPrice = el("div", "cart-detail");
  unitPrice.append(...priceParts(product));
  about.append(unitPrice);
  const subtotal = el("div", "cart-subtotal");
  if (!soldOut) subtotal.append(...priceParts(product, item.quantity));
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
      subtotal.replaceChildren(...priceParts(product, item.quantity));
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

function openCart(note = "") {
  $("cart-added").textContent = note;
  $("cart-added").hidden = !note;
  $("cart-dialog").showModal();
  refreshCart();
}

// An order that is finished, one way or another, can be ordered again.
function canBuyAgain(order) {
  return ["completed", "cancelled", "rejected"].includes(order.status);
}

// Puts an earlier order's items in the cart again, as far as they are still sold and in stock,
// and never more than the stock or the cart allows.
async function buyAgain(order) {
  const ids = [...new Set(order.items.map((item) => item.product_id))];
  const results = await Promise.allSettled(ids.map((id) => api(`/products/${id}`)));
  const failed = results.find((result) => result.status === "rejected" && result.reason.status !== 404);
  if (failed) throw failed.reason;
  const variants = new Map();
  for (const result of results) {
    if (result.status !== "fulfilled") continue;
    for (const variant of result.value.variants || []) variants.set(variant.id, variant);
  }
  const items = [];
  let unavailable = 0;
  let already = 0;
  for (const item of order.items) {
    const variant = variants.get(item.variant_id);
    if (!variant || variant.stock <= 0) {
      unavailable++;
      continue;
    }
    const inCart = cart.find((entry) => entry.variant_id === item.variant_id)?.quantity || 0;
    const room = Math.min(variant.stock, cartMaxQuantity) - inCart;
    if (room <= 0) already++;
    else items.push({ product_id: item.product_id, variant_id: item.variant_id, quantity: Math.min(item.quantity, room) });
  }
  const skipped = items.length ? await addToCart(items) : 0;
  return { added: items.length - skipped, already, unavailable, full: skipped };
}

function buyAgainMessage({ added, already, unavailable, full }) {
  const parts = [];
  if (added) parts.push(`${added} produk masuk keranjang.`);
  if (already) parts.push(`${already} produk sudah ada di keranjang sebanyak stoknya.`);
  if (unavailable) parts.push(`${unavailable} produk stoknya habis atau sudah tidak dijual, tidak dimasukkan.`);
  if (full) parts.push(`Keranjang penuh, ${full} produk tidak muat.`);
  return parts.join(" ");
}

// The Beli lagi button. When something went in, or was in the cart already, the cart opens
// with a note; otherwise the note is shown where the order is.
function buyAgainButton(order, className, showProblem, beforeCart) {
  const button = el("button", `${className} buy-again`);
  button.type = "button";
  button.append(chevron("M3 4h2l2.4 11h10.2L20 7H6.2 M9 20a1 1 0 1 0 0-2 1 1 0 0 0 0 2z M17 20a1 1 0 1 0 0-2 1 1 0 0 0 0 2z"), "Beli lagi");
  button.setAttribute("aria-label", `Beli lagi pesanan #${order.id}`);
  button.addEventListener("click", async () => {
    button.disabled = true;
    showProblem("");
    try {
      const result = await buyAgain(order);
      if (result.added || result.already) {
        if (beforeCart) beforeCart();
        openCart(buyAgainMessage(result));
      } else {
        showProblem(buyAgainMessage(result) || "Belum ada produk yang bisa dimasukkan ke keranjang.");
      }
    } catch (error) {
      showProblem(error.status === 401 ? "Masuk lagi dulu untuk memakai keranjang." : cartWriteMessage(error));
    } finally {
      button.disabled = false;
    }
  });
  return button;
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
const addressLimit = 10;
let checkoutItems = [];
let checkoutFromCart = false;
let checkoutBlocked = true;
// The account's addresses, and the one this order goes to.
let checkoutAddresses = [];
let savedAddress = null;
// The address the checkout form changes, or null while it adds a new one.
let addressEditId = null;
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
    api("/me/addresses"),
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
  if (failed || table.status === "rejected" || address.status === "rejected") {
    $("checkout-status").textContent = "Checkout belum bisa dimuat. Tutup lalu coba lagi sebentar lagi.";
    return;
  }
  checkoutAddresses = address.value.addresses;
  savedAddress = checkoutAddresses.find((a) => a.main) || checkoutAddresses[0] || null;
  addressEditId = savedAddress ? savedAddress.id : null;
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
    const detail = el("div", "cart-detail", `${variant.size} · ${variant.color} · ${item.quantity} × `);
    detail.append(...priceParts(product));
    about.append(el("div", "cart-name", product.name), detail);
    if (variant.stock < item.quantity) {
      problem = "Stok ada yang kurang dari jumlah pesananmu. Ubah jumlahnya di keranjang dulu.";
      about.append(el("p", "error", variant.stock === 0 ? "Stok habis" : `Stok tinggal ${variant.stock}`));
    }
    const lineTotal = product.price * item.quantity;
    subtotal += lineTotal;
    const lineCell = el("div", "cart-subtotal");
    lineCell.append(...priceParts(product, item.quantity));
    row.append(art, about, lineCell);
    return row;
  });
  $("checkout-items").replaceChildren(...rows);
  checkoutSubtotal = subtotal;
  $("checkout-problem").textContent = problem;
  $("checkout-problem").hidden = !problem;
  checkoutBlocked = Boolean(problem);
  $("checkout-submit").disabled = checkoutBlocked;
}

// Shows the chosen address, or the form when there's none yet or one is being added or changed.
function showAddress(editing) {
  $("address-form").hidden = !editing;
  $("address-view").hidden = editing;
  $("address-cancel").hidden = !(savedAddress && zoneOf(savedAddress.province));
  $("address-error").hidden = true;
  if (editing) {
    const adding = addressEditId === null;
    const address = adding
      ? { label: checkoutAddresses.length ? "" : "Rumah", recipient: currentUser ? currentUser.name : "", phone: currentUser ? currentUser.phone || "" : "" }
      : savedAddress;
    $("address-label").value = address.label || "";
    for (const field of addressFields) $(`address-${field}`).value = address[field] || "";
    // A province that isn't in the list leaves the choice empty.
    if (!zoneOf($("address-province").value)) $("address-province").value = "";
    $("address-form-title").hidden = !(adding && checkoutAddresses.length);
    $("address-form-title").textContent = "Alamat baru";
  } else {
    const a = savedAddress;
    const head = el("span", "address-head");
    head.append(el("strong", "", a.label));
    if (a.main) head.append(el("span", "badge-main", "Utama"));
    $("address-text").replaceChildren(head, ...[`${a.recipient} · ${a.phone}`, a.street, `${a.district}, ${a.city}`, `${a.province} ${a.postal_code}`].map((line) => el("span", "", line)));
  }
  renderShipping();
}

// Shows the chosen address, asking for the province again when it isn't one shipping knows.
function useAddress(address) {
  savedAddress = address;
  addressEditId = address.id;
  const known = zoneOf(address.province);
  showAddress(!known);
  if (!known) {
    $("address-error").textContent = "Pilih provinsi dari daftar supaya ongkir bisa dihitung.";
    $("address-error").hidden = false;
  }
}

function openAddressPicker() {
  const list = $("address-pick-list");
  list.replaceChildren(...checkoutAddresses.map((address) => {
    const option = el("label", "address-option");
    const radio = el("input");
    radio.type = "radio";
    radio.name = "pick-address";
    radio.value = String(address.id);
    radio.checked = savedAddress && address.id === savedAddress.id;
    const text = el("span", "address-option-text");
    const head = el("span", "address-head");
    head.append(el("strong", "", address.label));
    if (address.main) head.append(el("span", "badge-main", "Utama"));
    text.append(head, el("span", "address-option-name", `${address.recipient} · ${address.phone}`),
      el("span", "", `${address.street}, ${address.district}, ${address.city}, ${address.province} ${address.postal_code}`));
    option.append(radio, text);
    return option;
  }));
  const full = checkoutAddresses.length >= addressLimit;
  $("address-pick-new").disabled = full;
  $("address-pick-new").title = full ? "Sudah ada 10 alamat. Hapus salah satu di halaman Akun." : "";
  $("address-pick-dialog").showModal();
  const checked = list.querySelector("input:checked") || list.querySelector("input");
  if (checked) checked.focus();
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
  address.label = $("address-label").value.trim();
  // The answer is the address as stored, with the phone number in one form, and the whole list.
  const result = addressEditId === null
    ? await send("POST", "/me/addresses", address)
    : await send("PUT", `/me/addresses/${addressEditId}`, address);
  checkoutAddresses = result.addresses;
  savedAddress = result.address;
  addressEditId = savedAddress.id;
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
      : error.status === 409 ? "Sudah ada 10 alamat. Hapus salah satu di halaman Akun dulu."
      : error.status === 404 ? "Alamat ini sudah dihapus. Tutup checkout lalu buka lagi."
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
    const order = { items: checkoutItems.map(({ variant_id, quantity }) => ({ variant_id, quantity })), shipping_address: Object.fromEntries(addressFields.map((field) => [field, savedAddress[field]])), shipping_option: chosenShipping };
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
$("cart-open").addEventListener("click", () => openCart());
$("footer-cart").addEventListener("click", () => openCart());
$("footer-orders").addEventListener("click", () => { location.hash = "#akun/pesanan"; });
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
  addressEditId = savedAddress.id;
  showAddress(true);
  $("address-recipient").focus();
});
$("address-cancel").addEventListener("click", () => {
  addressEditId = savedAddress.id;
  showAddress(false);
  $("address-change").focus();
});
$("address-change").addEventListener("click", openAddressPicker);
$("address-pick-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const checked = $("address-pick-list").querySelector("input:checked");
  const address = checked && checkoutAddresses.find((a) => String(a.id) === checked.value);
  $("address-pick-dialog").close();
  if (address) useAddress(address);
  $("address-change").focus();
});
$("address-pick-new").addEventListener("click", () => {
  $("address-pick-dialog").close();
  addressEditId = null;
  showAddress(true);
  $("address-label").focus();
});
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
$("auth-form").addEventListener("submit", submitAuth);
$("auth-switch").addEventListener("click", () => setAuthMode(authMode === "login" ? "register" : "login"));
$("forgot-open").addEventListener("click", openForgot);
$("forgot-form").addEventListener("submit", submitForgot);
$("forgot-back").addEventListener("click", () => {
  $("forgot-dialog").close();
  openAuth();
});
$("reset-form").addEventListener("submit", submitReset);
$("verify-form").addEventListener("submit", submitVerify);
$("verify-resend").addEventListener("click", resendVerify);
window.addEventListener("hashchange", openResetFromLink);
for (const button of document.querySelectorAll("[data-close]")) {
  button.addEventListener("click", () => button.closest("dialog").close());
}

$("fav-open").addEventListener("click", openFavorites);
$("product-fav").addEventListener("click", () => {
  if (shownProductId !== null) toggleFavorite(shownProductId);
});
addStepper($("order-quantity"));
showCartCount();
markFavorites();
renderCategories();
loadAccount();
loadAuthOptions();
openResetFromLink();
openGoogleFailure();
loadFilterChoices();
loadPromo();
// Category names appear on the cards, so the list loads after them.
// The highlights sort by ratings and show category names, so they wait for both.
const shopReady = loadCategories().then(() => {
  loadProducts();
  loadFlash();
  loadLook();
  return loadRatings();
});
shopReady.then(() => homePicks.load());
$("product-rating").addEventListener("click", () => {
  $("product-reviews").scrollIntoView({ behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
});
$("review-more").addEventListener("click", () => loadReviews(reviews.productId, reviews.stars, true));
