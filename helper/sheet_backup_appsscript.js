// Apps Script untuk backup order agenpulsa -> Google Sheet.
// Cara pakai:
//  1. sheet.google.com > New sheet
//  2. Extensions > Apps Script > paste kode ini > Save
//  3. Deploy > New deployment > Web app > Execute as "Me", Who has access "Anyone"
//  4. Copy URL /exec -> paste di web admin agenpulsa -> Pengaturan -> Backup Google Sheet
//  5. Sheet header (baris 1): created_at, ref, nomor, label, modal, harga_jual, status, pesan, sumber, invoice_id

function doPost(e) {
  try {
    const data = JSON.parse(e.postData.contents);
    const ss = SpreadsheetApp.getActiveSpreadsheet();
    const sh = ss.getActiveSheet();
    if (sh.getLastRow() === 0) {
      sh.appendRow(['created_at','ref','nomor','label','modal','harga_jual','status','pesan','sumber','invoice_id']);
    }
    const rows = data.rows || [];
    for (let i = 0; i < rows.length; i++) {
      sh.appendRow(rows[i]);
    }
    return ContentService.createTextOutput(JSON.stringify({ok: true, added: rows.length}));
  } catch (err) {
    return ContentService.createTextOutput(JSON.stringify({ok: false, error: String(err)}));
  }
}
