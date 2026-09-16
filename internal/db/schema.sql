CREATE TABLE IF NOT EXISTS catalog (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    label TEXT NOT NULL,
    tab TEXT NOT NULL DEFAULT 'Paket Kuota',
    cari TEXT NOT NULL,
    voucher TEXT,
    operator TEXT,                          -- provider paket (guard nomor user)
    harga_max INTEGER NOT NULL DEFAULT 0,   -- modal, guard harga naik
    harga_jual INTEGER NOT NULL DEFAULT 0,  -- harga ke pelanggan
    aktif INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ref TEXT UNIQUE,                        -- idempotensi: dari client / invoice paypan
    nomor TEXT NOT NULL,
    catalog_id INTEGER REFERENCES catalog(id),
    label TEXT NOT NULL,
    modal INTEGER NOT NULL DEFAULT 0,
    harga_jual INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'queued',  -- pending_payment|queued|running|success|failed|cancelled
    order_id_isipulsa TEXT,
    pesan TEXT,
    invoice_id TEXT,                        -- id invoice paypan (pending_payment)
    sumber TEXT NOT NULL DEFAULT 'api',     -- api|telegram|wa|paypan|admin|scheduler
    chat_id TEXT,                           -- untuk callback ke client
    callback_url TEXT,
    created_at TEXT NOT NULL,               -- WIB ISO
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at);

CREATE TABLE IF NOT EXISTS schedules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tipe TEXT NOT NULL,                     -- harian|sekali|interval
    label TEXT NOT NULL,
    catalog_id INTEGER REFERENCES catalog(id),
    nomor TEXT NOT NULL,
    jam TEXT NOT NULL,                      -- HH:MM (harian/interval), DD/MM/YYYY HH:MM (sekali)
    interval_hari INTEGER NOT NULL DEFAULT 1,
    terakhir_jalan TEXT,
    aktif INTEGER NOT NULL DEFAULT 1,
    chat_id TEXT
);

CREATE TABLE IF NOT EXISTS contacts (
    nama TEXT PRIMARY KEY,
    nomor TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT
);

CREATE TABLE IF NOT EXISTS api_keys (
    key TEXT PRIMARY KEY,
    nama TEXT NOT NULL,
    boleh_order INTEGER NOT NULL DEFAULT 1,
    boleh_admin INTEGER NOT NULL DEFAULT 0
);
