// Minimal SHA-256 (FIPS 180-4) implementation with UTF-8 input via TextEncoder.
// Used as a fallback when WebCrypto is unavailable (e.g., non-secure contexts).
(function () {
  'use strict';

  function rotr(x, n) {
    return (x >>> n) | (x << (32 - n));
  }

  function ch(x, y, z) {
    return (x & y) ^ (~x & z);
  }

  function maj(x, y, z) {
    return (x & y) ^ (x & z) ^ (y & z);
  }

  function bigSigma0(x) {
    return rotr(x, 2) ^ rotr(x, 13) ^ rotr(x, 22);
  }

  function bigSigma1(x) {
    return rotr(x, 6) ^ rotr(x, 11) ^ rotr(x, 25);
  }

  function smallSigma0(x) {
    return rotr(x, 7) ^ rotr(x, 18) ^ (x >>> 3);
  }

  function smallSigma1(x) {
    return rotr(x, 17) ^ rotr(x, 19) ^ (x >>> 10);
  }

  var K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1,
    0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
    0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786,
    0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147,
    0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
    0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b,
    0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a,
    0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
    0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
  ]);

  function sha256Bytes(message) {
    var ml = message.length;
    var bitLenHi = (ml / 0x20000000) | 0; // (ml * 8) >>> 32, approximated via ml / 2^29
    var bitLenLo = (ml << 3) >>> 0;       // (ml * 8) mod 2^32

    // Pad: 0x80, then zeros, then 64-bit length
    var withOne = ml + 1;
    var padZeros = (withOne % 64 <= 56) ? (56 - (withOne % 64)) : (56 + 64 - (withOne % 64));
    var total = withOne + padZeros + 8;
    var padded = new Uint8Array(total);
    padded.set(message, 0);
    padded[ml] = 0x80;
    // Length (big-endian)
    var p = total - 8;
    padded[p + 0] = (bitLenHi >>> 24) & 0xff;
    padded[p + 1] = (bitLenHi >>> 16) & 0xff;
    padded[p + 2] = (bitLenHi >>> 8) & 0xff;
    padded[p + 3] = bitLenHi & 0xff;
    padded[p + 4] = (bitLenLo >>> 24) & 0xff;
    padded[p + 5] = (bitLenLo >>> 16) & 0xff;
    padded[p + 6] = (bitLenLo >>> 8) & 0xff;
    padded[p + 7] = bitLenLo & 0xff;

    // Initial hash values
    var h0 = 0x6a09e667, h1 = 0xbb67ae85, h2 = 0x3c6ef372, h3 = 0xa54ff53a;
    var h4 = 0x510e527f, h5 = 0x9b05688c, h6 = 0x1f83d9ab, h7 = 0x5be0cd19;

    var w = new Uint32Array(64);

    for (var i = 0; i < total; i += 64) {
      for (var t = 0; t < 16; t++) {
        var j = i + (t * 4);
        w[t] = ((padded[j] << 24) | (padded[j + 1] << 16) | (padded[j + 2] << 8) | padded[j + 3]) >>> 0;
      }
      for (var t2 = 16; t2 < 64; t2++) {
        var s0 = smallSigma0(w[t2 - 15]);
        var s1 = smallSigma1(w[t2 - 2]);
        w[t2] = (w[t2 - 16] + s0 + w[t2 - 7] + s1) >>> 0;
      }

      var a = h0, b = h1, c = h2, d = h3, e = h4, f = h5, g = h6, h = h7;

      for (var t3 = 0; t3 < 64; t3++) {
        var t1 = (h + bigSigma1(e) + ch(e, f, g) + K[t3] + w[t3]) >>> 0;
        var t0 = (bigSigma0(a) + maj(a, b, c)) >>> 0;
        h = g;
        g = f;
        f = e;
        e = (d + t1) >>> 0;
        d = c;
        c = b;
        b = a;
        a = (t1 + t0) >>> 0;
      }

      h0 = (h0 + a) >>> 0;
      h1 = (h1 + b) >>> 0;
      h2 = (h2 + c) >>> 0;
      h3 = (h3 + d) >>> 0;
      h4 = (h4 + e) >>> 0;
      h5 = (h5 + f) >>> 0;
      h6 = (h6 + g) >>> 0;
      h7 = (h7 + h) >>> 0;
    }

    return new Uint32Array([h0, h1, h2, h3, h4, h5, h6, h7]);
  }

  function toHex32(x) {
    return ('00000000' + x.toString(16)).slice(-8);
  }

  function sha256HexFallback(str) {
    var enc = new TextEncoder();
    var bytes = enc.encode(str);
    var h = sha256Bytes(bytes);
    return (
      toHex32(h[0]) + toHex32(h[1]) + toHex32(h[2]) + toHex32(h[3]) +
      toHex32(h[4]) + toHex32(h[5]) + toHex32(h[6]) + toHex32(h[7])
    );
  }

  async function sha256Hex(str) {
    // Prefer WebCrypto when available (faster), otherwise fall back to pure JS.
    try {
      if (window.crypto && crypto.subtle && typeof crypto.subtle.digest === 'function') {
        var buffer = new TextEncoder().encode(str);
        var hashBuffer = await crypto.subtle.digest('SHA-256', buffer);
        var hashArray = Array.from(new Uint8Array(hashBuffer));
        return hashArray.map(function (b) { return b.toString(16).padStart(2, '0'); }).join('');
      }
    } catch (_) {}
    return sha256HexFallback(str);
  }

  window.sha256Hex = sha256Hex;
})();

