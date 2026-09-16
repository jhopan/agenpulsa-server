package db

import (
	"database/sql"
	"time"
)

const wib = "Asia/Jakarta"

var wibLoc = mustLoc()

func mustLoc() *time.Location {
	loc, err := time.LoadLocation(wib)
	if err != nil {
		// Fallback: WIB = UTC+7 tetap.
		return time.FixedZone("WIB", 7*3600)
	}
	return loc
}

func NowWIB() time.Time { return time.Now().In(wibLoc) }

func WIBLoc() *time.Location { return wibLoc }

// ---------- Catalog ----------

type CatalogItem struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"`
	Tab       string `json:"tab"`
	Cari      string `json:"cari"`
	Voucher   string `json:"voucher"`
	Operator  string `json:"operator"` // provider paket (guard nomor user)
	HargaMax  int64  `json:"harga_max"`
	HargaJual int64  `json:"harga_jual"`
	Aktif     bool   `json:"aktif"`
}

func (s *Store) ListCatalog(onlyActive bool) ([]CatalogItem, error) {
	q := "SELECT id,label,tab,cari,COALESCE(voucher,''),COALESCE(operator,''),harga_max,harga_jual,aktif FROM catalog"
	if onlyActive {
		q += " WHERE aktif=1"
	}
	q += " ORDER BY id"
	rows, err := s.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogItem
	for rows.Next() {
		var it CatalogItem
		var aktif int
		if err := rows.Scan(&it.ID, &it.Label, &it.Tab, &it.Cari, &it.Voucher, &it.Operator, &it.HargaMax, &it.HargaJual, &aktif); err != nil {
			return nil, err
		}
		it.Aktif = aktif == 1
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) GetCatalog(id int64) (*CatalogItem, error) {
	var it CatalogItem
	var aktif int
	err := s.DB.QueryRow("SELECT id,label,tab,cari,COALESCE(voucher,''),COALESCE(operator,''),harga_max,harga_jual,aktif FROM catalog WHERE id=?", id).
		Scan(&it.ID, &it.Label, &it.Tab, &it.Cari, &it.Voucher, &it.Operator, &it.HargaMax, &it.HargaJual, &aktif)
	if err != nil {
		return nil, err
	}
	it.Aktif = aktif == 1
	return &it, nil
}

func (s *Store) UpsertCatalog(it *CatalogItem) (int64, error) {
	aktif := 0
	if it.Aktif {
		aktif = 1
	}
	if it.ID > 0 {
		_, err := s.DB.Exec("UPDATE catalog SET label=?,tab=?,cari=?,voucher=?,operator=?,harga_max=?,harga_jual=?,aktif=? WHERE id=?",
			it.Label, it.Tab, it.Cari, it.Voucher, it.Operator, it.HargaMax, it.HargaJual, aktif, it.ID)
		return it.ID, err
	}
	r, err := s.DB.Exec("INSERT INTO catalog(label,tab,cari,voucher,operator,harga_max,harga_jual,aktif) VALUES(?,?,?,?,?,?,?,?)",
		it.Label, it.Tab, it.Cari, it.Voucher, it.Operator, it.HargaMax, it.HargaJual, aktif)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) DeleteCatalog(id int64) error {
	_, err := s.DB.Exec("DELETE FROM catalog WHERE id=?", id)
	return err
}

// ---------- Orders ----------

type Order struct {
	ID          int64  `json:"id"`
	Ref         string `json:"ref"`
	Nomor       string `json:"nomor"`
	CatalogID   int64  `json:"catalog_id"`
	Label       string `json:"label"`
	Modal       int64  `json:"modal"`
	HargaJual   int64  `json:"harga_jual"`
	Status      string `json:"status"`
	OrderIDIsip string `json:"order_id_isipulsa"`
	Pesan       string `json:"pesan"`
	InvoiceID   string `json:"invoice_id"`
	Sumber      string `json:"sumber"`
	ChatID      string `json:"chat_id"`
	CallbackURL string `json:"callback_url"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

const orderCols = "id,COALESCE(ref,''),COALESCE(nomor,''),COALESCE(catalog_id,0),label,modal,harga_jual,status,COALESCE(order_id_isipulsa,''),COALESCE(pesan,''),COALESCE(invoice_id,''),sumber,COALESCE(chat_id,''),COALESCE(callback_url,''),created_at,updated_at"

func scanOrder(sc interface{ Scan(...any) error }) (*Order, error) {
	var o Order
	err := sc.Scan(&o.ID, &o.Ref, &o.Nomor, &o.CatalogID, &o.Label, &o.Modal, &o.HargaJual, &o.Status,
		&o.OrderIDIsip, &o.Pesan, &o.InvoiceID, &o.Sumber, &o.ChatID, &o.CallbackURL, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) CreateOrder(o *Order) (int64, error) {
	now := NowWIB().Format("2006-01-02 15:04:05")
	if o.CreatedAt == "" {
		o.CreatedAt, o.UpdatedAt = now, now
	}
	var catalogID any
	if o.CatalogID > 0 {
		catalogID = o.CatalogID
	}
	r, err := s.DB.Exec(`INSERT INTO orders(ref,nomor,catalog_id,label,modal,harga_jual,status,order_id_isipulsa,pesan,invoice_id,sumber,chat_id,callback_url,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.Ref, o.Nomor, catalogID, o.Label, o.Modal, o.HargaJual, o.Status, o.OrderIDIsip, o.Pesan, o.InvoiceID, o.Sumber, o.ChatID, o.CallbackURL, o.CreatedAt, o.UpdatedAt)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) GetOrder(id int64) (*Order, error) {
	return scanOrder(s.DB.QueryRow("SELECT "+orderCols+" FROM orders WHERE id=?", id))
}

func (s *Store) GetOrderByRef(ref string) (*Order, error) {
	return scanOrder(s.DB.QueryRow("SELECT "+orderCols+" FROM orders WHERE ref=?", ref))
}

func (s *Store) ListOrders(status string, limit int) ([]*Order, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := "SELECT " + orderCols + " FROM orders"
	var args []any
	if status != "" {
		q += " WHERE status=?"
		args = append(args, status)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) UpdateOrderStatus(id int64, status, pesan, orderIDIsip string) error {
	_, err := s.DB.Exec("UPDATE orders SET status=?,pesan=?,order_id_isipulsa=CASE WHEN ?='' THEN order_id_isipulsa ELSE ? END,updated_at=? WHERE id=?",
		status, pesan, orderIDIsip, orderIDIsip, NowWIB().Format("2006-01-02 15:04:05"), id)
	return err
}

// SetOrderInvoice simpan id invoice paypan ke order.
func (s *Store) SetOrderInvoice(id int64, invoiceID string) error {
	_, err := s.DB.Exec("UPDATE orders SET invoice_id=?,updated_at=? WHERE id=?",
		invoiceID, NowWIB().Format("2006-01-02 15:04:05"), id)
	return err
}

// GetOrderByInvoice cari order by invoice paypan (untuk webhook order.paid).
func (s *Store) GetOrderByInvoice(invoiceID string) (*Order, error) {
	return scanOrder(s.DB.QueryRow("SELECT "+orderCols+" FROM orders WHERE invoice_id=?", invoiceID))
}

// PopNextQueued ambil satu order terlama berstatus queued (FIFO).
func (s *Store) PopNextQueued() (*Order, error) {
	o, err := scanOrder(s.DB.QueryRow("SELECT " + orderCols + " FROM orders WHERE status='queued' ORDER BY id ASC LIMIT 1"))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return o, err
}

// ---------- Laporan ----------

type Laporan struct {
	Periode string `json:"periode"`
	Sukses  int    `json:"sukses"`
	Gagal   int    `json:"gagal"`
	Modal   int64  `json:"modal"`
	Omzet   int64  `json:"omzet"`
	Profit  int64  `json:"profit"`
}

func (s *Store) Report(days int) (*Laporan, error) {
	since := NowWIB().AddDate(0, 0, -days).Format("2006-01-02")
	l := &Laporan{}
	err := s.DB.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='success' THEN modal ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='success' THEN harga_jual ELSE 0 END),0)
		FROM orders WHERE substr(created_at,1,10)>=?`, since).
		Scan(&l.Sukses, &l.Gagal, &l.Modal, &l.Omzet)
	l.Profit = l.Omzet - l.Modal
	return l, err
}

// ---------- Schedules ----------

type Schedule struct {
	ID            int64  `json:"id"`
	Tipe          string `json:"tipe"`
	Label         string `json:"label"`
	CatalogID     int64  `json:"catalog_id"`
	Nomor         string `json:"nomor"`
	Jam           string `json:"jam"`
	IntervalHari  int    `json:"interval_hari"`
	TerakhirJalan string `json:"terakhir_jalan"`
	Aktif         bool   `json:"aktif"`
	ChatID        string `json:"chat_id"`
}

func (s *Store) ListSchedules() ([]Schedule, error) {
	rows, err := s.DB.Query(`SELECT id,tipe,label,COALESCE(catalog_id,0),nomor,jam,interval_hari,COALESCE(terakhir_jalan,''),aktif,COALESCE(chat_id,'') FROM schedules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		var aktif int
		if err := rows.Scan(&sc.ID, &sc.Tipe, &sc.Label, &sc.CatalogID, &sc.Nomor, &sc.Jam, &sc.IntervalHari, &sc.TerakhirJalan, &aktif, &sc.ChatID); err != nil {
			return nil, err
		}
		sc.Aktif = aktif == 1
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) UpsertSchedule(sc *Schedule) (int64, error) {
	aktif := 0
	if sc.Aktif {
		aktif = 1
	}
	if sc.ID > 0 {
		_, err := s.DB.Exec(`UPDATE schedules SET tipe=?,label=?,catalog_id=?,nomor=?,jam=?,interval_hari=?,terakhir_jalan=?,aktif=?,chat_id=? WHERE id=?`,
			sc.Tipe, sc.Label, sc.CatalogID, sc.Nomor, sc.Jam, sc.IntervalHari, sc.TerakhirJalan, aktif, sc.ChatID, sc.ID)
		return sc.ID, err
	}
	r, err := s.DB.Exec(`INSERT INTO schedules(tipe,label,catalog_id,nomor,jam,interval_hari,terakhir_jalan,aktif,chat_id) VALUES(?,?,?,?,?,?,?,?,?)`,
		sc.Tipe, sc.Label, sc.CatalogID, sc.Nomor, sc.Jam, sc.IntervalHari, sc.TerakhirJalan, aktif, sc.ChatID)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) DeleteSchedule(id int64) error {
	_, err := s.DB.Exec("DELETE FROM schedules WHERE id=?", id)
	return err
}

func (s *Store) SetScheduleLastRun(id int64, tanggal string) error {
	_, err := s.DB.Exec("UPDATE schedules SET terakhir_jalan=? WHERE id=?", tanggal, id)
	return err
}

// ---------- Contacts ----------

func (s *Store) ListContacts() (map[string]string, error) {
	rows, err := s.DB.Query("SELECT nama,nomor FROM contacts ORDER BY nama")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, no string
		if err := rows.Scan(&n, &no); err != nil {
			return nil, err
		}
		out[n] = no
	}
	return out, rows.Err()
}

func (s *Store) UpsertContact(nama, nomor string) error {
	_, err := s.DB.Exec("INSERT INTO contacts(nama,nomor) VALUES(?,?) ON CONFLICT(nama) DO UPDATE SET nomor=excluded.nomor", nama, nomor)
	return err
}

func (s *Store) DeleteContact(nama string) error {
	_, err := s.DB.Exec("DELETE FROM contacts WHERE nama=?", nama)
	return err
}

// ---------- Settings ----------

func (s *Store) GetSetting(key, def string) string {
	var v string
	err := s.DB.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&v)
	if err != nil || v == "" {
		return def
	}
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

// ---------- API keys ----------

type APIKey struct {
	Key        string `json:"key"`
	Nama       string `json:"nama"`
	BolehOrder bool   `json:"boleh_order"`
	BolehAdmin bool   `json:"boleh_admin"`
}

func (s *Store) CheckAPIKey(key string) (*APIKey, error) {
	var k APIKey
	var o, a int
	err := s.DB.QueryRow("SELECT key,nama,boleh_order,boleh_admin FROM api_keys WHERE key=?", key).Scan(&k.Key, &k.Nama, &o, &a)
	if err != nil {
		return nil, err
	}
	k.BolehOrder, k.BolehAdmin = o == 1, a == 1
	return &k, nil
}

func (s *Store) ListAPIKeys() ([]APIKey, error) {
	rows, err := s.DB.Query("SELECT key,nama,boleh_order,boleh_admin FROM api_keys ORDER BY nama")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var o, a int
		if err := rows.Scan(&k.Key, &k.Nama, &o, &a); err != nil {
			return nil, err
		}
		k.BolehOrder, k.BolehAdmin = o == 1, a == 1
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) UpsertAPIKey(k *APIKey) error {
	o, a := 0, 0
	if k.BolehOrder {
		o = 1
	}
	if k.BolehAdmin {
		a = 1
	}
	_, err := s.DB.Exec(`INSERT INTO api_keys(key,nama,boleh_order,boleh_admin) VALUES(?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET nama=excluded.nama,boleh_order=excluded.boleh_order,boleh_admin=excluded.boleh_admin`,
		k.Key, k.Nama, o, a)
	return err
}

func (s *Store) DeleteAPIKey(key string) error {
	_, err := s.DB.Exec("DELETE FROM api_keys WHERE key=?", key)
	return err
}
