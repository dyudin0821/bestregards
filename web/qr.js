// Build-time QR code for the landing page: an inline SVG, so the page ships no QR library and no image
// request. Dark modules are one path filled with currentColor; the CSS supplies the colours.
import QRCode from 'qrcode';

const MARGIN = 2; // quiet zone in modules; the light background of .qr .code extends it

export function qrSvg(text, label) {
  const {size, data} = QRCode.create(text, {errorCorrectionLevel: 'L'}).modules;
  let d = '';
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      if (!data[y * size + x]) continue;
      let run = 1;
      while (x + run < size && data[y * size + x + run]) run++;
      d += `M${x + MARGIN} ${y + MARGIN}h${run}v1h-${run}z`;
      x += run - 1;
    }
  }
  const box = size + 2 * MARGIN;
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${box} ${box}" shape-rendering="crispEdges" role="img" aria-label="${label}">` +
    `<path fill="currentColor" d="${d}"/></svg>`;
}

// Replaces every <div data-qr="URL" ...></div> with the same element holding the code of that URL.
export function injectQr(html) {
  return html.replace(/<div([^>]*?)\sdata-qr="([^"]+)"([^>]*)><\/div>/g, (_, before, url, after) =>
    `<div${before}${after}>${qrSvg(url, `QR-код: ${url}`)}</div>`);
}
