# AgenPulsa Admin, Design Direction

## Read

Admin operasional untuk pemilik agen pulsa. Bersih, terang, cepat dibaca saat memeriksa saldo, order, katalog, dan jadwal. Dials: ENERGY 1 / RHYTHM 2 / MOTION 1.

## Identity

- Karakter: utilitarian, tenang, terpercaya. Bukan landing page dan bukan dashboard finansial generik.
- Motif: garis hijau vertikal pendek pada navigasi aktif dan heading area kerja. Menandai konteks aktif tanpa dekorasi.
- Focal point: judul halaman dan tindakan utama pada area kerja.

## Palette

- Ink: `#14213d`, dipakai sidebar dan teks utama. Memberi kontras tinggi untuk layar operasional.
- Canvas: `#f6f8fb`, dipakai halaman agar kartu dan tabel terbaca tanpa putih menyilaukan.
- Surface: `#ffffff`, dipakai hanya untuk header, kartu, dan input.
- Accent: `#16803c`, dipakai untuk status berhasil, navigasi aktif, fokus, dan aksi utama.
- Danger: `#b42318`, dipakai hanya aksi destruktif dan status gagal.

## Typography and layout

- Typeface: system UI stack. Cepat, tersedia pada server tanpa font eksternal, mudah dibaca pada desktop dan HP.
- Shell: sidebar gelap desktop, header tipis, area konten maksimal 1120px. Hierarki datang dari ruang kosong dan bobot teks, bukan bayangan besar.
- Cards: radius 14px dengan border tipis. Shadow hanya pada hover kartu yang dapat dibuka.
- Tables: header lembut, zebra hover, konten padat. Scroll horizontal hanya pada tabel.
- Buttons: radius 9px, tinggi minimum 40px. Aksi primer hijau, sekunder putih berborder, aksi hapus merah.

## Responsive and motion

- Desktop sidebar tetap terlihat. Pada lebar 900px, sidebar berubah menjadi drawer dengan tombol Menu dan scrim yang dapat ditutup lewat klik atau Escape.
- Form dua kolom turun menjadi satu kolom pada layar kecil.
- Motion hanya transisi drawer dan hover singkat. Memudahkan orientasi, bukan dekorasi.

## States and accessibility

- Semua fokus keyboard memakai ring hijau terlihat.
- Kontras teks normal minimal WCAG AA.
- Elemen data tetap memakai state kosong yang sudah ada. Loading dan error memakai disabled action atau flash error sesuai alur sekarang.

## Decision log

- Warna hijau dipertahankan karena terkait status agen, bukan sebagai hiasan di seluruh layar.
- Sidebar gelap memisahkan navigasi dari data transaksi dan menjaga konteks saat halaman panjang.
- Tidak ada dark-mode toggle. Admin dipakai untuk membaca angka dan form dalam sesi kerja panjang, sehingga light canvas tetap konsisten.
- Tidak ada ikon dekoratif baru. Simbol yang ada adalah penanda tindakan atau menu.
