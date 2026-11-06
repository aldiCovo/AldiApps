"use strict";

// Voucher codes for checkout. Uses $, el, button, api, send, when, rupiah and handleAuth from
// admin.js, and field, textInput, numberInput, wholeNumber and setBusy from products.js.
// order-service checks every value again.

const maxVoucherRupiah = 100000000;
let vouchers = [];
let vouchersToken = 0;

const voucherKinds = { percent: "Diskon persen", amount: "Potongan rupiah", free_shipping: "Gratis ongkir" };

const voucherMessages = {
  "another voucher has this code": "Kode ini sudah dipakai voucher lain.",
  "orders use this voucher, so it can only be turned off": "Voucher ini sudah dipakai pesanan, jadi hanya bisa dinonaktifkan.",
  "voucher not found": "Voucher itu sudah tidak ada. Daftar dimuat ulang.",
};

function voucherError(error, fallback) {
  if (handleAuth(error)) return null;
  if (voucherMessages[error.message]) return voucherMessages[error.message];
  if (error.status === 400) return `Periksa lagi isinya: ${error.message}`;
  return fallback;
}

function voucherTerms(voucher) {
  const most = voucher.max_discount ? `, maksimal ${rupiah.format(voucher.max_discount)}` : "";
  const terms = voucher.kind === "percent" ? `Diskon ${voucher.value}%${most}`
    : voucher.kind === "amount" ? `Potongan ${rupiah.format(voucher.value)}`
    : `Gratis ongkir${most}`;
  return voucher.min_subtotal > 0 ? `${terms} · min. belanja ${rupiah.format(voucher.min_subtotal)}` : terms;
}

function voucherState(voucher) {
  const now = Date.now();
  if (!voucher.active) return ["Nonaktif", ""];
  if (now < Date.parse(voucher.starts_at)) return ["Belum mulai", ""];
  if (now >= Date.parse(voucher.ends_at)) return ["Berakhir", "bad"];
  if (voucher.quota && voucher.used >= voucher.quota) return ["Kuota habis", "bad"];
  return ["Aktif", "good"];
}

// datetime-local works in the browser's own time, which here is the shop's.
function localInput(value) {
  const input = el("input");
  input.type = "datetime-local";
  if (value) {
    const date = new Date(value);
    input.value = new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  }
  return input;
}

async function loadVouchers() {
  const token = ++vouchersToken;
  $("vouchers-status").textContent = "Memuat voucher…";
  try {
    const answer = await api("/vouchers");
    if (token !== vouchersToken) return;
    vouchers = answer.vouchers;
    $("voucher-list").replaceChildren(...vouchers.map(voucherRow));
    $("vouchers-status").textContent = vouchers.length === 0 ? "Belum ada voucher." : `${vouchers.length} voucher`;
  } catch (error) {
    if (token !== vouchersToken || handleAuth(error)) return;
    $("vouchers-status").textContent = "Voucher belum bisa dimuat. Coba lagi sebentar lagi.";
  }
}

function voucherRow(voucher) {
  const row = el("article", "voucher-row");
  const who = el("div", "voucher-who");
  who.append(el("strong", "voucher-code", voucher.code), el("span", "", voucherTerms(voucher)));
  if (voucher.description) who.append(el("span", "muted small", voucher.description));
  const facts = el("div", "voucher-facts");
  const [label, tone] = voucherState(voucher);
  facts.append(
    el("span", `state ${tone}`, label),
    el("span", "muted small", `${when(voucher.starts_at)} – ${when(voucher.ends_at)}`),
    el("span", "muted small", `Dipakai ${voucher.used}${voucher.quota ? ` dari ${voucher.quota}` : ""} · ${voucher.per_user_limit}× per akun`),
  );
  const actions = el("div", "voucher-actions");
  const status = el("p", "status voucher-status", "");
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");
  const edit = button("Ubah", "secondary small-button", () => {
    const form = voucherForm(voucher, (saved) => replaceVoucher(row, saved, "Voucher disimpan."), () => {
      form.remove();
      row.hidden = false;
      edit.focus();
    });
    row.hidden = true;
    row.after(form);
    form.querySelector("input").focus();
  });
  const toggle = button(voucher.active ? "Nonaktifkan" : "Aktifkan", "link", async () => {
    setBusy(row, true);
    try {
      const saved = await send("PUT", `/vouchers/${voucher.id}`, { ...voucherBody(voucher), active: !voucher.active });
      replaceVoucher(row, saved, saved.active ? "Voucher aktif lagi." : "Voucher dinonaktifkan.");
    } catch (error) {
      failVoucher(row, voucher, error, "Voucher belum bisa diubah. Coba lagi sebentar lagi.");
    }
  });
  actions.append(edit, toggle);
  if (voucher.used === 0) {
    const remove = button("Hapus", "link danger", () => {
      remove.hidden = true;
      const ask = el("div", "confirm");
      ask.append(el("p", "", `Hapus voucher ${voucher.code}? Kode ini tidak bisa dipakai lagi.`));
      const buttons = el("div", "form-actions");
      const yes = button("Ya, hapus", "primary danger-fill", async () => {
        setBusy(row, true);
        try {
          await send("DELETE", `/vouchers/${voucher.id}`);
          vouchers = vouchers.filter((item) => item.id !== voucher.id);
          row.remove();
          $("vouchers-status").textContent = `Voucher ${voucher.code} dihapus.`;
        } catch (error) {
          failVoucher(row, voucher, error, "Voucher belum bisa dihapus. Coba lagi sebentar lagi.");
        }
      });
      buttons.append(yes, button("Batal", "link", () => {
        ask.remove();
        remove.hidden = false;
        remove.focus();
      }));
      ask.append(buttons);
      actions.append(ask);
      yes.focus();
    });
    actions.append(remove);
  }
  row.append(who, facts, actions, status);
  return row;
}

function replaceVoucher(row, saved, message) {
  vouchers = vouchers.map((item) => (item.id === saved.id ? saved : item));
  const fresh = voucherRow(saved);
  fresh.querySelector(".voucher-status").textContent = message;
  const form = row.nextElementSibling;
  if (form && form.classList.contains("voucher-form")) form.remove();
  row.replaceWith(fresh);
}

function failVoucher(row, voucher, error, fallback) {
  const message = voucherError(error, fallback);
  if (message === null) return;
  if (error.status === 404) {
    loadVouchers().then(() => {
      $("vouchers-status").textContent = message;
    });
    return;
  }
  const fresh = voucherRow(voucher);
  fresh.querySelector(".voucher-status").textContent = message;
  row.replaceWith(fresh);
}

function voucherBody(voucher) {
  const { code, description, kind, value, max_discount, min_subtotal, quota, per_user_limit, starts_at, ends_at, active } = voucher;
  return { code, description, kind, value, max_discount, min_subtotal, quota, per_user_limit, starts_at, ends_at, active };
}

// The same form makes a voucher and changes one.
function voucherForm(voucher, onSaved, onCancel) {
  const form = el("form", "product-form voucher-form panel");
  form.noValidate = true;
  const code = textInput(voucher ? voucher.code : "", 20);
  code.autocapitalize = "characters";
  code.spellcheck = false;
  const description = textInput(voucher ? voucher.description : "", 200);
  const kind = el("select");
  for (const [id, label] of Object.entries(voucherKinds)) kind.append(new Option(label, id));
  kind.value = voucher ? voucher.kind : "percent";
  const value = numberInput(voucher && voucher.kind !== "free_shipping" ? voucher.value : "", 1, maxVoucherRupiah);
  const most = numberInput(voucher && voucher.max_discount ? voucher.max_discount : "", 1, maxVoucherRupiah);
  const minimum = numberInput(voucher ? voucher.min_subtotal : 0, 0, maxVoucherRupiah);
  const quota = numberInput(voucher && voucher.quota ? voucher.quota : "", 1, 1000000);
  const perUser = numberInput(voucher ? voucher.per_user_limit : 1, 1, 100);
  const starts = localInput(voucher ? voucher.starts_at : new Date().toISOString());
  const ends = localInput(voucher ? voucher.ends_at : new Date(Date.now() + 7 * 86400000).toISOString());
  const active = el("input");
  active.type = "checkbox";
  active.checked = voucher ? voucher.active : true;

  const valueField = field("", value);
  const mostField = field("Potongan maksimal (Rp, kosongkan jika tanpa batas)", most);
  const relabel = () => {
    valueField.firstChild.textContent = kind.value === "percent" ? "Diskon (%)" : "Potongan (Rp)";
    valueField.hidden = kind.value === "free_shipping";
    mostField.hidden = kind.value === "amount";
  };
  kind.addEventListener("change", relabel);
  relabel();

  const activeField = el("label", "check");
  activeField.append(active, document.createTextNode(" Aktif"));
  const error = el("p", "error", "");
  error.setAttribute("role", "alert");
  error.hidden = true;
  const save = el("button", "primary", voucher ? "Simpan perubahan" : "Simpan voucher");
  save.type = "submit";
  const actions = el("div", "form-actions");
  actions.append(save, button("Batal", "link", onCancel));
  const grid = el("div", "voucher-grid");
  grid.append(
    field("Kode (huruf dan angka)", code),
    field("Jenis", kind),
    valueField,
    mostField,
    field("Minimal belanja (Rp)", minimum),
    field("Kuota total (kosongkan jika tanpa batas)", quota),
    field("Batas per akun", perUser),
    field("Mulai", starts),
    field("Berakhir", ends),
  );
  form.append(
    el("h2", "", voucher ? `Ubah ${voucher.code}` : "Voucher baru"),
    grid,
    field("Keterangan untuk pembeli (opsional)", description),
    activeField,
    error,
    actions,
  );

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const isPercent = kind.value === "percent";
    const valueNumber = kind.value === "free_shipping" ? 0 : wholeNumber(value, 1, isPercent ? 100 : maxVoucherRupiah);
    const mostNumber = most.value.trim() === "" || kind.value === "amount" ? null : wholeNumber(most, 1, maxVoucherRupiah);
    const minimumNumber = wholeNumber(minimum, 0, maxVoucherRupiah);
    const quotaNumber = quota.value.trim() === "" ? null : wholeNumber(quota, 1, 1000000);
    const perUserNumber = wholeNumber(perUser, 1, 100);
    const startsAt = starts.value ? new Date(starts.value) : null;
    const endsAt = ends.value ? new Date(ends.value) : null;
    const problem = !/^[A-Za-z0-9]{3,20}$/.test(code.value.trim()) ? [code, "Kode harus 3 sampai 20 huruf atau angka, tanpa spasi."]
      : valueNumber === null ? [value, isPercent ? "Diskon harus angka bulat 1 sampai 100." : "Potongan harus angka bulat dari Rp 1."]
      : mostNumber === null && most.value.trim() !== "" && kind.value !== "amount" ? [most, "Potongan maksimal harus angka bulat dari Rp 1, atau kosong."]
      : minimumNumber === null ? [minimum, "Minimal belanja harus angka bulat, boleh 0."]
      : quotaNumber === null && quota.value.trim() !== "" ? [quota, "Kuota harus angka bulat dari 1, atau kosong."]
      : perUserNumber === null ? [perUser, "Batas per akun harus angka bulat 1 sampai 100."]
      : !startsAt || !endsAt ? [startsAt ? ends : starts, "Isi waktu mulai dan berakhir."]
      : endsAt <= startsAt ? [ends, "Waktu berakhir harus setelah waktu mulai."]
      : null;
    if (problem) {
      error.textContent = problem[1];
      error.hidden = false;
      problem[0].focus();
      return;
    }
    const body = {
      code: code.value.trim().toUpperCase(),
      description: description.value.trim(),
      kind: kind.value,
      value: valueNumber,
      max_discount: mostNumber,
      min_subtotal: minimumNumber,
      quota: quotaNumber,
      per_user_limit: perUserNumber,
      starts_at: startsAt.toISOString(),
      ends_at: endsAt.toISOString(),
      active: active.checked,
    };
    error.hidden = true;
    setBusy(form, true);
    try {
      const saved = voucher ? await send("PUT", `/vouchers/${voucher.id}`, body) : await send("POST", "/vouchers", body);
      onSaved(saved);
    } catch (failure) {
      const message = voucherError(failure, "Voucher belum bisa disimpan. Coba lagi sebentar lagi.");
      if (message === null) return;
      error.textContent = message;
      error.hidden = false;
      setBusy(form, false);
    }
  });
  return form;
}

$("new-voucher").addEventListener("click", () => {
  $("new-voucher").hidden = true;
  const form = voucherForm(null, (saved) => {
    form.remove();
    $("new-voucher").hidden = false;
    vouchers = [saved, ...vouchers];
    const row = voucherRow(saved);
    row.querySelector(".voucher-status").textContent = "Voucher dibuat.";
    $("voucher-list").prepend(row);
    $("vouchers-status").textContent = `${vouchers.length} voucher`;
  }, () => {
    form.remove();
    $("new-voucher").hidden = false;
    $("new-voucher").focus();
  });
  $("voucher-list").before(form);
  form.querySelector("input").focus();
});
