# gibahin.id

gibahin.id adalah jejaring sosial privat berbasis Go, MySQL, dan PWA. Postingan dan chat hanya dibuka untuk akun yang berteman.

## Menyiapkan MySQL di Laragon

1. Jalankan MySQL dari Laragon.
2. Import [`database/schema.sql`](database/schema.sql) melalui HeidiSQL atau klien MySQL. File ini membuat database `temanaja` beserta tabelnya.
3. Buka file `.env` di direktori proyek dan ganti `ISI_PASSWORD_MYSQL_DI_SINI` dengan kata sandi MySQL Laragon. `.env` tidak dimasukkan ke Git.

Jika MySQL root tidak memakai kata sandi, bagian sebelum `@tcp` cukup `root:`. File `.env.example` bisa dipakai sebagai referensi untuk SMTP dan pengaturan lainnya.

Setelah mengedit `.env`, jalankan `go run .` dari direktori proyek.

Buka `http://localhost:8080`. Aplikasi juga menyiapkan tabel yang belum ada saat startup. Atur `MYSQL_DSN` jika konfigurasi MySQL-mu berbeda, serta `ADDR` untuk mengubah port server.

## Email verifikasi

Pendaftaran meminta nama, email, dan kata sandi. Setelah daftar, pengguna memasukkan kode 4 digit yang dikirim ke email. Kode berlaku 10 menit, maksimal 5 kali percobaan, dan kode baru bisa diminta setiap 30 detik. Setelah berhasil, pengguna langsung masuk ke langkah username, foto profil, lalu pencarian teman (bisa dilewati).

Untuk development tanpa SMTP, kode verifikasi dicetak di terminal tempat `go run .` berjalan. Agar kode benar-benar terkirim ke email, isi konfigurasi SMTP di `.env` (gunakan port 587/STARTTLS dan kata sandi aplikasi dari penyedia email):

```powershell
$env:SMTP_HOST = "smtp.example.com"
$env:SMTP_PORT = "587"
$env:SMTP_USERNAME = "akun@example.com"
$env:SMTP_PASSWORD = "kata-sandi-aplikasi"
$env:SMTP_FROM = "akun@example.com"
```

Untuk tautan di email, atur `APP_URL` ke alamat gibahin.id yang bisa dibuka penerima. Untuk domain publik, gunakan alamat HTTPS. Saat dibuka dari ponsel di jaringan lokal, gunakan alamat IP komputer di jaringan tersebut, misalnya `http://192.168.1.10:8080`.

Di halaman masuk, pilih **Lupa kata sandi?** untuk meminta tautan reset. Tautan reset berlaku 30 menit dan hanya dapat digunakan sekali. Setelah kata sandi berhasil diganti, sesi login lama akan dikeluarkan.

## Fitur

- Daftar, verifikasi email, login, onboarding profil, dan foto profil.
- Cari teman, kirim/terima/tolak permintaan; menambahkan teman saat onboarding bisa dilewati.
- Postingan teks/foto dengan feed akun sendiri dan teman yang telah diterima; pemilik dapat mengedit postingan selama 30 menit atau menghapusnya.
- Edit profil: nama, username, bio, dan foto profil.
- Chat pribadi berbasis polling dengan teman yang sudah diterima.
- Antarmuka responsif dan bisa dipasang sebagai PWA. Kerangka antarmuka tersimpan untuk akses cepat; API dan chat memerlukan koneksi.

Kata sandi dan kode verifikasi email disimpan dalam bentuk hash. Kode kedaluwarsa setelah 10 menit. Pesan dan foto disimpan di MySQL; batasi akses database dan gunakan kredensial SMTP khusus untuk deployment.

Jika memakai Gmail, SMTP_PASSWORD harus berupa App Password Google (akun harus memakai Verifikasi 2 Langkah), bukan password login Gmail. Masukkan alamat Gmail lengkap pada SMTP_USERNAME dan gunakan App Password yang baru dibuat. Google dapat mencabut App Password setelah password akun diubah.
