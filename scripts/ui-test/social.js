// Makes .github/social-preview.png (1280x640): name, pitch and a dashboard
// frame from demo.js, drawn by headless Chrome.
// Usage: node social.js <demo-frame.png> [out.png]
const fs = require('fs');
const path = require('path');
const puppeteer = require('puppeteer-core');
const { chromePath } = require('./lib');

const frame = process.argv[2];
const out = path.resolve(process.argv[3] || path.join(__dirname, '..', '..', '.github', 'social-preview.png'));
if (!frame) { console.error('usage: node social.js <demo-frame.png> [out.png]'); process.exit(2); }
const shot = 'data:image/png;base64,' + fs.readFileSync(frame).toString('base64');

const html = `<!doctype html><meta charset="utf-8"><style>
*{box-sizing:border-box}body{margin:0;width:1280px;height:640px;background:#f5f6f8;font-family:system-ui,sans-serif;color:#1c2128;overflow:hidden;position:relative}
.t{position:absolute;left:64px;top:88px;width:520px}
h1{font-size:64px;margin:0 0 24px;color:#2d6a4f}
p{font-size:30px;line-height:1.35;margin:0}
.s{position:absolute;left:640px;top:96px;width:720px;border-radius:12px;box-shadow:0 12px 40px rgba(0,0,0,.22);border:1px solid #d0d7de;display:block}
</style><div class="t"><h1>gh-runners</h1><p>Self-hosted GitHub Actions runners on your own Mac, Linux and Windows machines, set up in one click.</p></div><img class="s" src="${shot}">`;

(async () => {
  const browser = await puppeteer.launch({ executablePath: chromePath(), headless: true, args: ['--no-first-run'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 1280, height: 640 });
  await page.setContent(html, { waitUntil: 'load' });
  await page.screenshot({ path: out });
  await browser.close();
  console.log('wrote', out);
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1); });
