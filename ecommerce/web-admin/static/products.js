"use strict";

// Products, variants, prices and stock. Uses $, el, button, api, send, rupiah and handleAuth
// from admin.js, and the photo parts from photos.js. catalog-service checks every change again.

const productPage = 100;
const maxPrice = 1_000_000_000;
const maxStockChange = 100_000;

let categories = [];
let productFilter = "";
let products = [];
let productsToken = 0;

const catalogMessages = {
  "a category with this name already exists": "Nama kategori itu sudah ada.",
  "category still has products": "Kategori ini masih punya produk. Pindahkan atau hapus produknya dulu.",
  "this product already has a variant with this size and color": "Produk ini sudah punya varian dengan ukuran dan warna itu.",
  "stock would leave the allowed range": "Stok tidak boleh kurang dari 0.",
  "category_id does not exist": "Kategori itu sudah tidak ada. Muat ulang halaman.",
  "the sale price must be below the regular price": "Harga coret harus lebih rendah dari harga normal.",
  "quota must be empty or between 1 and 100000": "Kuota Flash Sale harus 1 sampai 100.000.",
  "the price must stay above the sale price; change or remove the sale first":
    "Harga normal harus di atas harga promo. Ubah atau hapus harga coretnya dulu.",
  "ends_at must be in the future": "Waktu selesai sudah lewat.",
  "ends_at must be after starts_at": "Waktu selesai harus setelah waktu mulai.",
};

// Turns a refusal into words for the admin. A refused session goes to the login.
function catalogError(error, fallback) {
  if (handleAuth(error)) return null;
  if (catalogMessages[error.message]) return catalogMessages[error.message];
  if (error.status === 404) return "Data ini sudah tidak ada. Muat ulang halaman.";
  return fallback;
}

function categoryName(id) {
  const category = categories.find((item) => item.id === id);
  return category ? category.name : "Tanpa kategori";
}

function field(label, input) {
  const wrap = el("label", "", label);
  wrap.append(input);
  return wrap;
}

function textInput(value, maxLength) {
  const input = el("input");
  input.type = "text";
  input.value = value;
  input.maxLength = maxLength;
  return input;
}

function numberInput(value, min, max) {
  const input = el("input");
  input.type = "number";
  input.inputMode = "numeric";
  input.min = String(min);
  input.max = String(max);
  input.step = "1";
  input.value = value === "" ? "" : String(value);
  return input;
}

function wholeNumber(input, min, max) {
  const text = input.value.trim();
  if (!/^-?[0-9]+$/.test(text)) return null;
  const number = Number(text);
  return number >= min && number <= max ? number : null;
}

function setBusy(box, busy) {
  for (const control of box.querySelectorAll("button, input, select, textarea")) control.disabled = busy;
}

// The same form adds a product and changes one.
function productForm(product, onSaved, onCancel) {
  const form = el("form", "product-form");
  form.noValidate = true;
  const name = textInput(product ? product.name : "", 200);
  name.required = true;
  const category = el("select");
  category.append(new Option("Tanpa kategori", ""));
  for (const item of categories) category.append(new Option(item.name, String(item.id)));
  category.value = product && product.category_id ? String(product.category_id) : "";
  // price is what customers pay now, which is the sale price during a sale. The form changes
  // the regular price.
  const price = numberInput(product ? product.regular_price ?? product.price : "", 1, maxPrice);
  const description = el("textarea");
  description.rows = 3;
  description.maxLength = 5000;
  description.value = product ? product.description : "";
  const error = el("p", "error", "");
  error.setAttribute("role", "alert");
  error.hidden = true;
  const save = el("button", "primary", product ? "Simpan perubahan" : "Simpan produk");
  save.type = "submit";
  const actions = el("div", "form-actions");
  actions.append(save);
  if (onCancel) actions.append(button("Batal", "link", onCancel));
  form.append(
    field("Nama produk", name),
    field("Kategori", category),
    field("Harga (Rp)", price),
    field("Deskripsi", description),
    error,
    actions,
  );
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const priceValue = wholeNumber(price, 1, maxPrice);
    const problem = !name.value.trim() ? [name, "Isi nama produk."]
      : priceValue === null ? [price, "Harga harus angka bulat dari 1 sampai 1.000.000.000."]
      : null;
    if (problem) {
      error.textContent = problem[1];
      error.hidden = false;
      problem[0].focus();
      return;
    }
    const body = {
      name: name.value.trim(),
      price: priceValue,
      description: description.value.trim(),
      category_id: category.value ? Number(category.value) : null,
    };
    error.hidden = true;
    setBusy(form, true);
    try {
      const saved = product ? await send("PUT", `/products/${product.id}`, body) : await send("POST", "/products", body);
      onSaved(saved);
    } catch (failure) {
      const message = catalogError(failure, "Produk belum bisa disimpan. Coba lagi sebentar lagi.");
      if (message === null) return;
      error.textContent = message;
      error.hidden = false;
      setBusy(form, false);
    }
  });
  return form;
}

async function loadProducts(more = false) {
  const token = ++productsToken;
  if (!more) {
    products = [];
    $("product-list").replaceChildren();
  }
  $("products-status").textContent = "Memuat produk…";
  $("more-products").hidden = true;
  try {
    const query = new URLSearchParams({ limit: String(productPage), offset: String(products.length) });
    if (productFilter) query.set("category_id", productFilter);
    const [categoryList, page] = await Promise.all([
      more ? Promise.resolve({ categories }) : api("/categories"),
      api(`/products?${query}`),
      more ? null : loadPhotoLists(),
    ]);
    if (token !== productsToken) return;
    categories = categoryList.categories;
    renderCategoryFilter();
    renderCategoryRows();
    products = products.concat(page.products);
    $("product-list").append(...page.products.map(productCard));
    $("more-products").hidden = page.products.length < productPage;
    $("products-status").textContent = products.length === 0 ? "Belum ada produk di sini." : "";
  } catch (error) {
    if (token !== productsToken || handleAuth(error)) return;
    $("products-status").textContent = "Produk belum bisa dimuat. Coba lagi sebentar lagi.";
  }
}

function renderCategoryFilter() {
  const box = $("category-filter");
  const chip = (label, value) => {
    const node = button(label, "tab", () => {
      productFilter = value;
      loadProducts();
    });
    node.setAttribute("aria-pressed", String(productFilter === value));
    return node;
  };
  if (productFilter && !categories.some((item) => String(item.id) === productFilter)) productFilter = "";
  box.replaceChildren(chip("Semua", ""), ...categories.map((item) => chip(item.name, String(item.id))));
}

function renderCategoryRows() {
  $("category-rows").replaceChildren(...categories.map(categoryRow));
}

function categoryRow(category) {
  const row = el("form", "category-row");
  row.noValidate = true;
  const name = textInput(category.name, 200);
  name.setAttribute("aria-label", `Nama kategori ${category.name}`);
  const save = el("button", "secondary", "Simpan");
  save.type = "submit";
  const remove = button("Hapus", "link danger", async () => {
    if (remove.dataset.confirm !== "yes") {
      remove.dataset.confirm = "yes";
      remove.textContent = `Yakin hapus ${category.name}?`;
      return;
    }
    await changeCategory(row, "DELETE", `/categories/${category.id}`, undefined, `Kategori ${category.name} dihapus.`);
  });
  row.append(name, save, remove, categoryPhotoTools(category));
  row.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!name.value.trim()) {
      $("categories-status").textContent = "Nama kategori tidak boleh kosong.";
      name.focus();
      return;
    }
    await changeCategory(row, "PUT", `/categories/${category.id}`, { name: name.value.trim() }, "Kategori disimpan.");
  });
  return row;
}

async function changeCategory(box, method, path, body, done) {
  setBusy(box, true);
  try {
    await send(method, path, body);
    $("categories-status").textContent = done;
    await loadProducts();
  } catch (error) {
    const message = catalogError(error, "Kategori belum bisa diubah. Coba lagi sebentar lagi.");
    if (message === null) return;
    $("categories-status").textContent = message;
    setBusy(box, false);
  }
}

$("new-category").addEventListener("submit", async (event) => {
  event.preventDefault();
  const name = $("new-category-name").value.trim();
  if (!name) {
    $("categories-status").textContent = "Isi nama kategori.";
    $("new-category-name").focus();
    return;
  }
  await changeCategory($("new-category"), "POST", "/categories", { name }, `Kategori ${name} ditambahkan.`);
  if ($("categories-status").textContent.endsWith("ditambahkan.")) $("new-category-name").value = "";
  setBusy($("new-category"), false);
});

function togglePanel(toggle, panel, onOpen) {
  toggle.addEventListener("click", () => {
    const open = panel.hidden;
    panel.hidden = !open;
    toggle.setAttribute("aria-expanded", String(open));
    if (open && onOpen) onOpen();
  });
}

togglePanel($("toggle-categories"), $("categories-panel"), () => $("new-category-name").focus());
togglePanel($("toggle-new-product"), $("new-product-panel"), () => {
  const close = () => {
    $("new-product-panel").hidden = true;
    $("toggle-new-product").setAttribute("aria-expanded", "false");
    $("toggle-new-product").focus();
  };
  $("new-product-form").replaceChildren(productForm(null, (saved) => {
    close();
    $("products-status").textContent = `Produk ${saved.name} ditambahkan. Tambahkan varian dan stoknya di bawah.`;
    products = [saved, ...products];
    $("product-list").prepend(productCard(saved));
    const card = $("product-list").querySelector(`[data-product="${saved.id}"]`);
    card.scrollIntoView({ block: "center" });
    card.querySelector(".variant-new input").focus();
  }, close));
  $("new-product-form").querySelector("input").focus();
});
$("more-products").addEventListener("click", () => loadProducts(true));

// Draws one product again from catalog-service after a change, so the page shows what's stored.
async function refreshProduct(card, id, note) {
  try {
    const fresh = await api(`/products/${id}`);
    products = products.map((item) => (item.id === id ? fresh : item));
    const next = productCard(fresh, note);
    card.replaceWith(next);
    return next;
  } catch (error) {
    if (handleAuth(error)) return null;
    if (error.status === 404) {
      card.remove();
      $("products-status").textContent = "Produk itu sudah dihapus di tempat lain.";
    }
    return null;
  }
}

function productCard(product, note = "") {
  const card = el("article", "order product");
  card.dataset.product = product.id;
  const status = el("p", "status card-status", note);
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");

  const head = el("div", "order-head");
  const title = el("div", "product-title");
  title.append(el("h2", "", product.name), el("span", "muted small", `${categoryName(product.category_id)} · #${product.id}`));
  const actions = el("div", "product-actions");
  const price = el("strong", "price num");
  if (product.original_price) price.append(el("s", "muted price-was", rupiah.format(product.original_price)), " ");
  price.append(rupiah.format(product.price));
  head.append(title, price, actions);
  card.append(head);
  if (product.description) card.append(el("p", "description muted", product.description));
  card.append(saleSection(product, card, status));
  card.append(photoSection(product, status));

  const editBox = el("div", "edit-box");
  editBox.hidden = true;
  const edit = button("Ubah produk", "secondary", () => {
    editBox.hidden = !editBox.hidden;
    edit.setAttribute("aria-expanded", String(!editBox.hidden));
    if (!editBox.hidden) {
      editBox.replaceChildren(productForm(product, (saved) => {
        refreshProduct(card, saved.id, "Produk disimpan.");
      }, () => {
        editBox.hidden = true;
        edit.setAttribute("aria-expanded", "false");
        edit.focus();
      }));
      editBox.querySelector("input").focus();
    }
  });
  edit.setAttribute("aria-expanded", "false");
  const remove = button("Hapus produk", "link danger", () => {
    const ask = el("div", "confirm");
    ask.append(el("p", "", `Hapus ${product.name}? Semua varian dan stoknya ikut terhapus dan produk hilang dari toko. Pesanan lama tetap menyimpan nama dan harganya.`));
    const yes = button("Ya, hapus", "primary danger-fill", async () => {
      setBusy(card, true);
      try {
        await send("DELETE", `/products/${product.id}`);
        products = products.filter((item) => item.id !== product.id);
        delete productPhotos[product.id];
        card.remove();
        $("products-status").textContent = `Produk ${product.name} dihapus.`;
      } catch (error) {
        const message = catalogError(error, "Produk belum bisa dihapus. Coba lagi sebentar lagi.");
        if (message === null) return;
        status.textContent = message;
        setBusy(card, false);
        ask.remove();
      }
    });
    const no = button("Tidak", "link", () => {
      ask.remove();
      remove.focus();
    });
    const buttons = el("div", "form-actions");
    buttons.append(yes, no);
    ask.append(buttons);
    remove.after(ask);
    yes.focus();
  });
  actions.append(edit, remove);
  card.append(editBox);

  const table = el("div", "variants");
  const header = el("div", "variant-row variant-head");
  header.append(el("span", "", "Ukuran · warna"), el("span", "num", "Stok"), el("span", "", "Ubah stok"), el("span", "", ""));
  table.append(header);
  if (product.variants.length === 0) table.append(el("p", "muted small", "Belum ada varian. Produk tanpa varian tidak bisa dibeli."));
  for (const variant of product.variants) table.append(variantRow(product, variant, card, status));
  card.append(table, newVariantForm(product, card, status), status);
  return card;
}

// saleState says where a product's sale is, from the browser's clock.
function saleState(sale) {
  const now = Date.now();
  if (now < Date.parse(sale.starts_at)) return ["Belum mulai", ""];
  if (now >= Date.parse(sale.ends_at)) return ["Berakhir", "bad"];
  return ["Berjalan", "good"];
}

// saleSection shows the product's "harga coret" and lets the admin set, change or remove it.
// Customers pay the sale price from its start until its end; the regular price is struck through.
function saleSection(product, card, status) {
  const box = el("div", "sale-box");
  const summary = el("div", "sale-summary");
  const sale = product.sale;
  if (sale) {
    const [label, tone] = saleState(sale);
    const percent = Math.floor((1 - sale.price / product.regular_price) * 100);
    const soldOut = sale.flash && sale.sold >= sale.quota && label === "Berjalan";
    summary.append(
      el("span", "", sale.flash ? "Flash Sale" : "Harga coret"),
      el("strong", "num", `${rupiah.format(sale.price)}${percent >= 1 ? ` (hemat ${percent}%)` : ""}`),
      el("span", "muted small", `${when(sale.starts_at)} – ${when(sale.ends_at)}`),
    );
    if (sale.flash) summary.append(el("span", "muted small", `Terjual ${sale.sold} dari kuota ${sale.quota}`));
    summary.append(soldOut ? el("span", "state bad", "Kuota habis") : el("span", `state ${tone}`, label));
  } else {
    summary.append(el("span", "muted small", "Tidak ada harga coret."));
  }
  const formBox = el("div", "edit-box");
  formBox.hidden = true;
  const toggle = button(sale ? "Ubah harga coret" : "Atur harga coret", "link", () => {
    formBox.hidden = !formBox.hidden;
    toggle.setAttribute("aria-expanded", String(!formBox.hidden));
    if (!formBox.hidden) {
      formBox.replaceChildren(saleForm(product, card, () => {
        formBox.hidden = true;
        toggle.setAttribute("aria-expanded", "false");
        toggle.focus();
      }));
      formBox.querySelector("input").focus();
    }
  });
  toggle.setAttribute("aria-expanded", "false");
  summary.append(toggle);
  if (sale) {
    const remove = button("Hapus harga coret", "link danger", async () => {
      if (remove.dataset.confirm !== "yes") {
        remove.dataset.confirm = "yes";
        remove.textContent = "Yakin hapus harga coret?";
        return;
      }
      setBusy(box, true);
      try {
        await send("DELETE", `/products/${product.id}/sale`);
        await refreshProduct(card, product.id, "Harga coret dihapus. Harga kembali normal.");
      } catch (error) {
        const message = catalogError(error, "Harga coret belum bisa dihapus. Coba lagi sebentar lagi.");
        if (message === null) return;
        status.textContent = message;
        setBusy(box, false);
      }
    });
    summary.append(remove);
  }
  box.append(summary, formBox);
  return box;
}

function saleForm(product, card, onCancel) {
  const form = el("form", "inline-form sale-form");
  form.noValidate = true;
  const sale = product.sale;
  const regular = product.regular_price ?? product.price;
  const price = numberInput(sale ? sale.price : "", 1, maxPrice);
  // localInput comes from vouchers.js.
  const starts = localInput(sale ? sale.starts_at : new Date().toISOString());
  const ends = localInput(sale ? sale.ends_at : new Date(Date.now() + 7 * 86400000).toISOString());
  const flash = el("input");
  flash.type = "checkbox";
  flash.checked = Boolean(sale && sale.flash);
  const flashField = el("label", "check");
  flashField.append(flash, document.createTextNode(" Flash Sale (tampil di beranda dengan kuota)"));
  const quota = numberInput(sale && sale.quota ? sale.quota : "", 1, 100000);
  const quotaField = field("Kuota Flash Sale (jumlah barang dengan harga ini)", quota);
  const showQuota = () => { quotaField.hidden = !flash.checked; };
  flash.addEventListener("change", showQuota);
  showQuota();
  const error = el("p", "error", "");
  error.setAttribute("role", "alert");
  error.hidden = true;
  const save = el("button", "secondary", "Simpan harga coret");
  save.type = "submit";
  form.append(
    field(`Harga promo (Rp), normal ${rupiah.format(regular)}`, price),
    field("Mulai", starts),
    field("Selesai", ends),
    flashField,
    quotaField,
    save,
    button("Batal", "link", onCancel),
    error,
  );
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const value = wholeNumber(price, 1, maxPrice);
    const startsAt = starts.value ? new Date(starts.value) : null;
    const endsAt = ends.value ? new Date(ends.value) : null;
    const quotaNumber = flash.checked ? wholeNumber(quota, 1, 100000) : null;
    const problem = value === null ? [price, "Harga promo harus angka bulat dari 1."]
      : flash.checked && quotaNumber === null ? [quota, "Kuota Flash Sale harus angka bulat 1 sampai 100.000."]
      : value >= regular ? [price, `Harga promo harus lebih rendah dari harga normal ${rupiah.format(regular)}.`]
      : !startsAt || !endsAt ? [startsAt ? ends : starts, "Isi waktu mulai dan selesai."]
      : endsAt <= startsAt ? [ends, "Waktu selesai harus setelah waktu mulai."]
      : endsAt.getTime() <= Date.now() ? [ends, "Waktu selesai sudah lewat."]
      : null;
    if (problem) {
      error.textContent = problem[1];
      error.hidden = false;
      problem[0].focus();
      return;
    }
    error.hidden = true;
    setBusy(form, true);
    try {
      await send("PUT", `/products/${product.id}/sale`, {
        price: value, starts_at: startsAt.toISOString(), ends_at: endsAt.toISOString(), quota: quotaNumber,
      });
      await refreshProduct(card, product.id, "Harga coret disimpan.");
    } catch (failure) {
      const message = catalogError(failure, "Harga coret belum bisa disimpan. Coba lagi sebentar lagi.");
      if (message === null) return;
      error.textContent = message;
      error.hidden = false;
      setBusy(form, false);
    }
  });
  return form;
}

function variantRow(product, variant, card, status) {
  const row = el("div", "variant-row");
  const label = el("span", "variant-name", `${variant.size} · ${variant.color}`);
  const stock = el("span", `num stock${variant.stock === 0 ? " empty" : ""}`, String(variant.stock));

  const change = el("form", "stock-change");
  change.noValidate = true;
  const amount = numberInput(1, 1, maxStockChange);
  amount.setAttribute("aria-label", `Jumlah stok ${variant.size} ${variant.color}`);
  const add = el("button", "secondary small-button", "+ Tambah");
  add.type = "submit";
  add.value = "1";
  const take = el("button", "secondary small-button", "− Kurangi");
  take.type = "submit";
  take.value = "-1";
  change.append(amount, add, take);
  change.addEventListener("submit", async (event) => {
    event.preventDefault();
    const count = wholeNumber(amount, 1, maxStockChange);
    if (count === null) {
      status.textContent = `Jumlah stok harus angka bulat dari 1 sampai ${maxStockChange.toLocaleString("id-ID")}.`;
      amount.focus();
      return;
    }
    const sign = Number(event.submitter ? event.submitter.value : 1);
    // A change rather than a new total, so orders placed meanwhile aren't overwritten.
    setBusy(row, true);
    try {
      const updated = await send("PATCH", `/variants/${variant.id}/stock`, { delta: sign * count });
      await refreshProduct(card, product.id, `Stok ${variant.size} · ${variant.color} sekarang ${updated.stock}.`);
    } catch (error) {
      const message = catalogError(error, "Stok belum bisa diubah. Coba lagi sebentar lagi.");
      if (message === null) return;
      status.textContent = message;
      setBusy(row, false);
    }
  });

  const tools = el("div", "variant-tools");
  const rename = button("Ubah", "link", () => {
    const form = el("form", "inline-form variant-edit");
    form.noValidate = true;
    const size = textInput(variant.size, 50);
    const color = textInput(variant.color, 50);
    const save = el("button", "secondary small-button", "Simpan");
    save.type = "submit";
    form.append(field("Ukuran", size), field("Warna", color), save, button("Batal", "link", () => {
      form.remove();
      rename.focus();
    }));
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!size.value.trim() || !color.value.trim()) {
        status.textContent = "Isi ukuran dan warna.";
        return;
      }
      setBusy(form, true);
      try {
        await send("PUT", `/variants/${variant.id}`, { size: size.value.trim(), color: color.value.trim() });
        await refreshProduct(card, product.id, "Varian disimpan.");
      } catch (error) {
        const message = catalogError(error, "Varian belum bisa disimpan. Coba lagi sebentar lagi.");
        if (message === null) return;
        status.textContent = message;
        setBusy(form, false);
      }
    });
    row.after(form);
    size.focus();
  });
  const remove = button("Hapus", "link danger", async () => {
    if (remove.dataset.confirm !== "yes") {
      remove.dataset.confirm = "yes";
      remove.textContent = "Yakin hapus?";
      return;
    }
    setBusy(row, true);
    try {
      await send("DELETE", `/variants/${variant.id}`);
      await refreshProduct(card, product.id, `Varian ${variant.size} · ${variant.color} dihapus.`);
    } catch (error) {
      const message = catalogError(error, "Varian belum bisa dihapus. Coba lagi sebentar lagi.");
      if (message === null) return;
      status.textContent = message;
      setBusy(row, false);
    }
  });
  tools.append(rename, remove);
  row.append(label, stock, change, tools);
  return row;
}

function newVariantForm(product, card, status) {
  const form = el("form", "inline-form variant-new");
  form.noValidate = true;
  const size = textInput("", 50);
  size.placeholder = "mis. M";
  const color = textInput("", 50);
  color.placeholder = "mis. Hitam";
  const stock = numberInput(0, 0, maxStockChange);
  const add = el("button", "secondary", "Tambah varian");
  add.type = "submit";
  form.append(field("Ukuran", size), field("Warna", color), field("Stok awal", stock), add);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const count = wholeNumber(stock, 0, maxStockChange);
    if (!size.value.trim() || !color.value.trim()) {
      status.textContent = "Isi ukuran dan warna varian baru.";
      (size.value.trim() ? color : size).focus();
      return;
    }
    if (count === null) {
      status.textContent = `Stok awal harus angka bulat dari 0 sampai ${maxStockChange.toLocaleString("id-ID")}.`;
      stock.focus();
      return;
    }
    setBusy(form, true);
    try {
      await send("POST", `/products/${product.id}/variants`, { size: size.value.trim(), color: color.value.trim(), stock: count });
      const next = await refreshProduct(card, product.id, `Varian ${size.value.trim()} · ${color.value.trim()} ditambahkan.`);
      if (next) next.querySelector(".variant-new input").focus();
    } catch (error) {
      const message = catalogError(error, "Varian belum bisa ditambahkan. Coba lagi sebentar lagi.");
      if (message === null) return;
      status.textContent = message;
      setBusy(form, false);
    }
  });
  return form;
}
