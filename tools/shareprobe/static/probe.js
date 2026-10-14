// picmover share probe: the self-running checklist. Plain ES2017 on
// purpose (no modules, no optional chaining), so it runs and reports even
// where the Svelte bundle does not.
(function () {
  'use strict';

  var cfg = {};
  try { cfg = JSON.parse(document.getElementById('cfg').textContent); } catch (e) { cfg = {}; }

  var ITEMS = [
    ['1.fragment', 'Token survives the certificate warning; cookie set by POST'],
    ['2.cookie', 'Cookie sent on fetch, navigation, reload, after reopening'],
    ['3.media-cookie', 'Cookie sent on <img>, <video>, Range, download'],
    ['4.video', 'Video playback with Range; download of an original'],
    ['5.engine', 'Svelte 5 bundle mounts; JS/CSS features'],
    ['6.local', '.local host name loads'],
    ['7.cert-persist', 'Certificate exception persists'],
    ['8.swipe', 'Swipes at the screen edges vs the middle'],
    ['9.cache', 'Thumbnail comes from the HTTP cache'],
    ['10.ca-constraints', 'Installed CA: name constraints enforced']
  ];

  var LS = 'shareprobe.v1.';
  function lsGet(k) { try { return JSON.parse(localStorage.getItem(LS + k)); } catch (e) { return null; } }
  function lsSet(k, v) { try { localStorage.setItem(LS + k, JSON.stringify(v)); } catch (e) { /* private mode */ } }
  function ssGet(k) { try { return JSON.parse(sessionStorage.getItem(LS + k)); } catch (e) { return null; } }
  function ssSet(k, v) { try { sessionStorage.setItem(LS + k, JSON.stringify(v)); } catch (e) { /* ignore */ } }

  // items: key → {status, detail, data}. Kept in localStorage so a later
  // visit reports the whole picture.
  var items = lsGet('items') || {};
  if (lsGet('server') !== cfg.urls.ip) { items = {}; } // another probe run/CA: start over
  lsSet('server', cfg.urls.ip);

  var errors = [];
  var csp = [];
  window.addEventListener('error', function (e) {
    errors.push(String(e.message || e.type) + (e.filename ? ' @' + e.filename + ':' + e.lineno : ''));
  }, true);
  window.addEventListener('unhandledrejection', function (e) { errors.push('unhandled rejection: ' + String(e.reason)); });
  document.addEventListener('securitypolicyviolation', function (e) {
    csp.push(e.violatedDirective + ' ' + (e.blockedURI || ''));
  });

  var run = Math.random().toString(36).slice(2, 10) + Date.now().toString(36);
  var $ = function (id) { return document.getElementById(id); };

  function set(key, status, detail, data) {
    var prev = items[key] || {};
    items[key] = { status: status, detail: detail || '', data: data || prev.data || null, at: new Date().toISOString() };
    lsSet('items', items);
    render();
  }

  function render() {
    var ol = $('items');
    if (!ol) { return; }
    ol.innerHTML = '';
    ITEMS.forEach(function (it) {
      var v = items[it[0]] || { status: 'WAIT', detail: '' };
      var li = document.createElement('li');
      li.className = 'item';
      var b = document.createElement('div');
      b.className = 'badge ' + v.status;
      b.textContent = v.status;
      var body = document.createElement('div');
      var t = document.createElement('div');
      t.className = 't';
      t.textContent = it[0].split('.')[0] + '. ' + it[1];
      var d = document.createElement('div');
      d.className = 'd';
      d.textContent = v.detail;
      body.appendChild(t);
      body.appendChild(d);
      li.appendChild(b);
      li.appendChild(body);
      ol.appendChild(li);
    });
    $('raw').textContent = JSON.stringify({ cfg: cfg, items: items, errors: errors, csp: csp }, null, 1);
  }

  function api(path, opts) {
    opts = opts || {};
    opts.credentials = 'same-origin';
    opts.cache = 'no-store';
    if (opts.body) {
      opts.method = 'POST';
      opts.headers = { 'Content-Type': 'application/json', 'X-Picmover': '1' };
      opts.body = JSON.stringify(opts.body);
    }
    return fetch(path, opts).then(function (r) {
      return r.json().then(function (j) { j._status = r.status; return j; }, function () { return { _status: r.status }; });
    });
  }

  function sleep(ms) { return new Promise(function (res) { setTimeout(res, ms); }); }

  var extra = {}; // non-item data for the report
  (function () { // download facts survive later visits
    var d4 = (items['4.video'] && items['4.video'].data) || {};
    if (d4.download) { extra.download = d4.download; }
    if (d4.downloadSaved) { extra.downloadSaved = d4.downloadSaved; }
  })();
  var phase = 'auto';

  var sending = Promise.resolve();
  function report(ph) {
    if (ph) { phase = ph; }
    var body = {
      phase: phase,
      device: cfg.device || lsGet('device') || '',
      run: run,
      page: { path: location.pathname, host: location.host, cookieSentOnLoad: cfg.cookieSent, secFetchSite: cfg.secFetchSite, secFetchMode: cfg.secFetchMode, secFetchDest: cfg.secFetchDest, secFetchUser: cfg.secFetchUser, proto: cfg.proto },
      ua: navigator.userAgent,
      screen: { w: screen.width, h: screen.height, iw: window.innerWidth, ih: window.innerHeight, dpr: window.devicePixelRatio },
      items: items,
      extra: extra,
      errors: errors.slice(0, 50),
      csp: csp.slice(0, 50)
    };
    sending = sending.then(function () {
      return api('/api/results', { body: body }).then(function (j) {
        $('sent').textContent = j.saved ? 'Results sent at ' + new Date().toLocaleTimeString() + ' (' + phase + ').' : 'Sending failed: HTTP ' + j._status;
      }, function (e) {
        $('sent').textContent = 'Sending failed: ' + e;
      });
    });
    return sending;
  }

  function banner(text) {
    var b = $('banner');
    b.hidden = !text;
    b.textContent = text || '';
  }

  // ---- item 1: pairing ----------------------------------------------------

  function pairWith(body, how) {
    return api('/api/pair', { body: body }).then(function (j) {
      if (j._status !== 200) { throw new Error('pair: HTTP ' + j._status + ' ' + (j.error || '')); }
      lsSet('device', j.device);
      lsSet('pairedAt', new Date().toISOString());
      // The cookie must be HttpOnly: invisible to script.
      var visible = document.cookie.indexOf('__Host-picmover') >= 0;
      return api('/api/whoami').then(function (w) {
        var ok = w.cookieSent && w.device === j.device;
        var viaText = {
          token: 'token read from location.hash after the warning',
          'fragment-code': 'typed code read from location.hash after the warning',
          'code-after-loss': 'fragment LOST on the way; paired with the typed code',
          code: 'paired with the typed code (no link with a fragment was used, so fragment survival is untested)'
        }[how];
        var d = viaText + '; cookie ' + (ok ? 'accepted and sent back' : 'NOT sent back') + (visible ? '; WARNING: cookie visible to script' : '; HttpOnly (not visible to script)');
        var fragOK = how === 'token' || how === 'fragment-code';
        var st = !ok || visible || how === 'code-after-loss' ? 'FAIL' : fragOK ? 'PASS' : 'INFO';
        set('1.fragment', st, d, { via: how, cookieBack: ok, scriptVisible: visible });
        return ok;
      });
    });
  }

  function pairPage() {
    var hash = location.hash.replace(/^#/, '');
    ssSet('session', true);
    var isToken = /^[A-Za-z0-9_-]{43}$/.test(hash);
    var isCode = /^[2-9A-Za-z]{4}-?[2-9A-Za-z]{4}$/.test(hash);
    if (isToken || isCode) {
      history.replaceState(null, '', '/pair'); // the token leaves the address bar and history
      banner('Pairing…');
      pairWith(isToken ? { token: hash } : { code: hash }, isToken ? 'token' : 'fragment-code').then(afterPair, function (e) {
        set('1.fragment', 'FAIL', String(e));
        showPairForm('Pairing with the token failed (' + e.message + '). Type the code from the terminal.');
      });
    } else {
      set('1.fragment', 'FAIL', 'No token in location.hash after loading (' + (hash ? 'malformed' : 'empty') + '): the fragment was lost on the way.', { via: 'none', hash: hash.length });
      showPairForm('The pairing token did not arrive (fragment lost). Type the code shown in the terminal.');
      report('pair-no-fragment');
    }
  }

  function showPairForm(msg) {
    $('pairbox').hidden = false;
    $('pairmsg').textContent = msg;
    banner('');
  }

  function bindCodeForm() {
    $('codeform').addEventListener('submit', function (e) {
      e.preventDefault();
      var c = $('code').value;
      ssSet('session', true);
      var lost = location.pathname === '/pair' && items['1.fragment'] && items['1.fragment'].data && items['1.fragment'].data.via === 'none';
      pairWith({ code: c }, lost ? 'code-after-loss' : 'code')
        .then(afterPair, function (err) { $('pairmsg').textContent = String(err); });
    });
  }

  function afterPair(ok) {
    if (!ok) {
      showPairForm('The cookie was not sent back. Results so far were sent; you can try the code.');
      report('pair-failed');
      return;
    }
    // Item 2: a scripted top-level navigation, then a reload.
    var st = { fetch: true };
    ssSet('steps', st);
    banner('Paired. Checking navigation and reload…');
    report('paired').then(function () { location.assign('/check?step=nav'); });
  }

  // ---- item 2: cookie on fetch / navigation / reload / reopen -------------

  function cookieSteps() {
    var st = ssGet('steps') || {};
    var q = location.search;
    if (q.indexOf('step=nav') >= 0 && st.nav === undefined) {
      st.nav = !!cfg.cookieSent;
      ssSet('steps', st);
      history.replaceState(null, '', '/check?step=reload');
      location.reload();
      return true; // navigating away
    }
    if (q.indexOf('step=reload') >= 0 && st.reload === undefined) {
      st.reload = !!cfg.cookieSent;
      ssSet('steps', st);
      history.replaceState(null, '', '/check');
    }
    return false;
  }

  function item2(fetchOK) {
    var st = ssGet('steps') || {};
    var prev = (items['2.cookie'] && items['2.cookie'].data) || {};
    var data = {
      fetch: fetchOK,
      nav: st.nav !== undefined ? st.nav : prev.nav,
      reload: st.reload !== undefined ? st.reload : prev.reload,
      reopen: prev.reopen,
      reopenAfterHours: prev.reopenAfterHours,
      thisLoad: !!cfg.cookieSent
    };
    function yn(v) { return v === undefined || v === null ? 'not yet' : v ? 'yes' : 'NO'; }
    var detail = 'fetch: ' + yn(data.fetch) + '; top-level navigation: ' + yn(data.nav) + '; reload: ' + yn(data.reload) +
      '; after reopening the browser: ' + yn(data.reopen) + (data.reopenAfterHours ? ' (' + data.reopenAfterHours + ' h after pairing)' : '');
    var vals = [data.fetch, data.nav, data.reload, data.reopen];
    var status = vals.some(function (v) { return v === false; }) ? 'FAIL' : vals.every(function (v) { return v === true; }) ? 'PASS' : 'WAIT';
    if (status === 'WAIT' && data.reopen === undefined && data.fetch && data.nav && data.reload) {
      detail += '\n→ Close the browser completely, reopen it and open ' + cfg.urls.ip + '/ again.';
    }
    set('2.cookie', status, detail, data);
  }

  function hoursSince(iso) {
    var t = Date.parse(iso || '');
    return isNaN(t) ? null : Math.round((Date.now() - t) / 360000) / 10;
  }

  function laterPanel() {
    // Shown on every visit that is not the pairing session.
    if (ssGet('session')) { return; }
    $('later').hidden = false;
    var dev = lsGet('device');
    $('laterinfo').textContent = 'This load ' + (cfg.cookieSent ? 'carried' : 'did NOT carry') + ' the cookie' +
      (dev ? '; paired ' + hoursSince(lsGet('pairedAt')) + ' h ago as ' + dev : '; this browser has no record of pairing') + '.';
  }

  function bindAnswers() {
    var buttons = document.querySelectorAll('button[data-answer]');
    Array.prototype.forEach.call(buttons, function (btn) {
      btn.addEventListener('click', function () {
        var key = btn.getAttribute('data-answer');
        var val = btn.getAttribute('data-value');
        Array.prototype.forEach.call(document.querySelectorAll('button[data-answer="' + key + '"]'), function (b) { b.classList.remove('chosen'); });
        btn.classList.add('chosen');
        answer(key, val);
      });
    });
  }

  function answer(key, val) {
    var h = hoursSince(lsGet('pairedAt'));
    if (key === '2.reopen') {
      var d2 = (items['2.cookie'] && items['2.cookie'].data) || {};
      if (val === 'yes') {
        d2.reopen = !!cfg.cookieSent;
        d2.reopenAfterHours = h;
      }
      items['2.cookie'] = { status: 'WAIT', detail: '', data: d2 };
      item2(items['2.cookie'].data.fetch);
      report('reopen');
    } else if (key === '7.cert-persist') {
      var hist = ((items['7.cert-persist'] && items['7.cert-persist'].data) || {}).answers || [];
      hist.push({ answer: val, hoursAfterPairing: h, at: new Date().toISOString() });
      set('7.cert-persist', val === 'nowarn' ? 'PASS' : 'FAIL',
        (val === 'nowarn' ? 'No warning on this visit' : 'The warning came back') + ' (' + h + ' h after pairing). History: ' +
        hist.map(function (a) { return a.answer + '@' + a.hoursAfterPairing + 'h'; }).join(', '), { answers: hist });
      report('cert-persist');
    } else if (key === '4.download') {
      extra.downloadSaved = val;
      checkDownload(val);
    } else if (key === '6.local') {
      set('6.local', 'FAIL', 'The .local page did not load (name not resolved, or the certificate was refused).', { loaded: false });
      report('local');
    } else if (key === '10.ca-installed' || key === '10.ca-constraints') {
      var d10 = (items['10.ca-constraints'] && items['10.ca-constraints'].data) || {};
      if (key === '10.ca-installed') { d10.installedTrusted = val === 'trusted'; } else { d10.outsideWarned = val === 'warned'; }
      item10(d10);
      report('ca');
    }
  }

  // ---- items 3 and 4: media ------------------------------------------------

  function imageTest() {
    return new Promise(function (res) {
      var img = $('img');
      var done = false;
      function fin(ok) { if (!done) { done = true; extra.imgLoaded = ok; res(ok); } }
      img.onload = function () { fin(true); };
      img.onerror = function () { fin(false); };
      img.src = '/m/img.png?run=' + run;
      setTimeout(function () { fin(false); }, 8000);
    });
  }

  var clipState = {};

  function videoTests() {
    var box = $('videos');
    box.innerHTML = '';
    var clips = cfg.clips || [];
    if (!clips.length) {
      box.textContent = 'No clips (ffmpeg missing on the computer?).';
      return Promise.resolve();
    }
    return Promise.all(clips.map(function (c) {
      return new Promise(function (res) {
        var row = document.createElement('div');
        row.className = 'clip';
        var v = document.createElement('video');
        v.setAttribute('playsinline', '');
        v.setAttribute('controls', '');
        v.muted = true;
        v.autoplay = true;
        v.preload = 'auto';
        var badge = document.createElement('div');
        badge.className = 'badge WAIT';
        badge.textContent = '…';
        var d = document.createElement('div');
        d.className = 'd';
        var s = { label: c.label, canPlayType: '', events: [], error: null, w: 0, h: 0, status: 'WAIT' };
        try { s.canPlayType = v.canPlayType(c.mime) || '""'; } catch (e) { s.canPlayType = 'error'; }
        clipState[c.name] = s;
        function upd() {
          s.w = v.videoWidth; s.h = v.videoHeight;
          var played = s.events.indexOf('playing') >= 0 || s.events.indexOf('timeupdate') >= 0;
          var can = s.events.indexOf('canplay') >= 0 || s.events.indexOf('loadeddata') >= 0;
          s.status = s.error ? 'FAIL' : played || (can && s.w > 0) ? 'PASS' : 'WAIT';
          badge.className = 'badge ' + s.status;
          badge.textContent = s.status;
          d.textContent = c.label + '\ncanPlayType: ' + s.canPlayType + '\n' + (s.error ? 'error ' + s.error + '\n' : '') +
            'events: ' + s.events.join(' ') + (s.w ? '\n' + s.w + '×' + s.h : '');
        }
        ['loadedmetadata', 'loadeddata', 'canplay', 'playing', 'timeupdate', 'stalled', 'ended'].forEach(function (ev) {
          v.addEventListener(ev, function () {
            if (s.events.indexOf(ev) < 0) { s.events.push(ev); }
            upd();
            if (ev === 'playing' || ev === 'timeupdate') { res(); }
          });
        });
        v.addEventListener('error', function () {
          var e = v.error;
          s.error = e ? e.code + (e.message ? ' ' + e.message : '') : 'unknown';
          upd();
          res();
        });
        v.src = '/m/clip/' + c.name + '?run=' + run;
        row.appendChild(v);
        row.appendChild(badge);
        row.appendChild(d);
        box.appendChild(row);
        upd();
        var pr = v.play && v.play();
        if (pr && pr.catch) { pr.catch(function (e) { s.events.push('play() rejected: ' + e.name); upd(); }); }
        setTimeout(res, 15000);
      });
    }));
  }

  function item4() {
    var names = Object.keys(clipState);
    var h264 = clipState['h264-aac.mp4'];
    var lines = names.map(function (n) { return clipState[n].status + '  ' + clipState[n].label + (clipState[n].error ? ' (error ' + clipState[n].error + ')' : ''); });
    var dl = extra.download ? '\ndownload: ' + extra.download : '\ndownload: tap "Download original" below';
    var st = !h264 ? (names.length ? 'INFO' : 'SKIP') : h264.status === 'PASS' ? 'PASS' : h264.status === 'FAIL' ? 'FAIL' : 'WAIT';
    if (extra.downloadSaved === 'failed') { st = 'FAIL'; }
    var data = {};
    names.forEach(function (n) { data[n] = clipState[n]; });
    set('4.video', st, lines.join('\n') + dl, { clips: data, download: extra.download || null, downloadSaved: extra.downloadSaved || null });
  }

  function obsFor() { return api('/api/obs?run=' + run); }

  function item3(obs) {
    var parts = [];
    var bad = false;
    var seen = 0;
    function line(label, o) {
      if (!o) { parts.push(label + ': no request seen'); return; }
      seen++;
      if (o.noCookie) { bad = true; }
      parts.push(label + ': ' + o.requests + ' request(s), ' + o.withCookie + ' with cookie' +
        (o.noCookie ? ', ' + o.noCookie + ' WITHOUT' : '') + (o.ranges ? '; Range ' + o.ranges + ' (' + o.rangesWithCookie + ' with cookie)' : ''));
    }
    line('<img>', obs.img);
    var vids = Object.keys(obs).filter(function (k) { return k.indexOf('clip:') === 0; });
    var agg = null;
    vids.forEach(function (k) {
      var o = obs[k];
      agg = agg || { requests: 0, withCookie: 0, noCookie: 0, ranges: 0, rangesWithCookie: 0 };
      agg.requests += o.requests; agg.withCookie += o.withCookie; agg.noCookie += o.noCookie; agg.ranges += o.ranges; agg.rangesWithCookie += o.rangesWithCookie;
    });
    line('<video>', agg);
    var prevDl = items['3.media-cookie'] && items['3.media-cookie'].data && items['3.media-cookie'].data.download;
    if (!obs.download && prevDl) { obs.download = prevDl; }
    line('download', obs.download);
    var noRange = agg && !agg.ranges;
    if (noRange) { parts.push('(no Range request seen for video)'); }
    var st = bad ? 'FAIL' : seen >= 2 && !noRange ? 'PASS' : 'WAIT';
    if (!obs.download && st === 'PASS') { parts.push('→ tap "Download original" to complete'); }
    set('3.media-cookie', st, parts.join('\n'), obs);
  }

  function checkDownload(saved) {
    return sleep(1500).then(obsFor).then(function (obs) {
      var o = obs.download;
      extra.download = o ? (o.withCookie ? 'request carried the cookie' : 'request WITHOUT cookie') : 'no request reached the server';
      item3(obs);
      item4();
      return report(saved ? 'download-answer' : 'download');
    });
  }

  // ---- item 5: engine ------------------------------------------------------

  function features() {
    function t(f) { try { return !!f(); } catch (e) { return false; } }
    function css(p, v) { try { return !!(window.CSS && CSS.supports && CSS.supports(p, v)); } catch (e) { return false; } }
    function sel(s) { try { return !!(window.CSS && CSS.supports && CSS.supports('selector(' + s + ')')); } catch (e) { return false; } }
    var f = {
      'Object.hasOwn': t(function () { return typeof Object.hasOwn === 'function'; }),
      structuredClone: t(function () { return typeof structuredClone === 'function' && structuredClone({ a: [1] }).a[0] === 1; }),
      'Array.prototype.findLast': t(function () { return [1, 2].findLast(function (x) { return x < 2; }) === 1; }),
      'Array.prototype.toSorted': t(function () { return typeof [].toSorted === 'function'; }),
      'Array.prototype.at': t(function () { return [1, 2].at(-1) === 2; }),
      'String.prototype.replaceAll': t(function () { return 'aa'.replaceAll('a', 'b') === 'bb'; }),
      'dvh units': css('height', '100dvh'),
      ':has()': sel(':has(a)'),
      'container queries': css('container-type', 'inline-size'),
      'CSS nesting': sel('&'),
      'color-mix()': css('color', 'color-mix(in srgb, red 50%, blue)'),
      'env(safe-area-inset-top)': css('padding-top', 'env(safe-area-inset-top)'),
      'touch-action': css('touch-action', 'none'),
      IntersectionObserver: typeof IntersectionObserver === 'function',
      ResizeObserver: typeof ResizeObserver === 'function',
      PointerEvent: typeof PointerEvent === 'function',
      TouchEvent: t(function () { return 'ontouchstart' in window || typeof TouchEvent === 'function'; }),
      'fetch keepalive': t(function () { return new Request('/ping', { keepalive: true }).keepalive === true; }),
      'Intl.DateTimeFormat': t(function () { return typeof new Intl.DateTimeFormat('en', { month: 'short' }).format(new Date()) === 'string'; }),
      'Intl dateStyle': t(function () { return new Intl.DateTimeFormat('en', { dateStyle: 'medium' }).resolvedOptions().dateStyle === 'medium'; }),
      'Intl formatToParts': t(function () { return new Intl.DateTimeFormat('en').formatToParts(new Date()).length > 0; }),
      'Intl timeZone': t(function () { return !!new Intl.DateTimeFormat().resolvedOptions().timeZone; }),
      'ES modules': t(function () { return 'noModule' in document.createElement('script'); })
    };
    return f;
  }

  function item5() {
    var f = features();
    extra.features = f;
    extra.env = {
      hoverNone: matchMedia('(hover: none)').matches,
      pointerCoarse: matchMedia('(pointer: coarse)').matches,
      maxTouchPoints: navigator.maxTouchPoints || 0,
      reducedMotion: matchMedia('(prefers-reduced-motion: reduce)').matches
    };
    var waited = 0;
    return new Promise(function (res) {
      (function poll() {
        var sp = window.__svelteProbe;
        if (sp || waited >= 8000) {
          var missing = Object.keys(f).filter(function (k) { return f[k] === false; });
          var st = sp && sp.mounted && sp.reactive ? 'PASS' : 'FAIL';
          var d = 'Svelte 5 component: ' + (sp ? (sp.mounted ? 'mounted' : 'NOT mounted') + (sp.reactive ? ', reactive update ok' : ', reactive update FAILED') + (sp.error ? ', error: ' + sp.error : '') : 'bundle did not run (no module support or a syntax/runtime error)') +
            (sp && sp.version ? ' (svelte ' + sp.version + ')' : '') +
            '\nmissing: ' + (missing.length ? missing.join(', ') : 'none');
          set('5.engine', st, d, { svelte: sp || null, features: f });
          res();
          return;
        }
        waited += 200;
        setTimeout(poll, 200);
      })();
    });
  }

  // ---- item 6: .local -------------------------------------------------------

  function localPage() {
    // Loaded on the .local name (another host: no cookie here).
    var dev = (location.hash.match(/dev=([A-Za-z0-9_]+)/) || [])[1] || '';
    if (dev) { cfg.device = dev; }
    set('6.local', 'PASS', 'Loaded at ' + location.host + ' (cookie ' + (cfg.cookieSent ? 'present' : 'absent, as expected for another host') + ').', { loaded: true, host: location.host });
    banner('.local works on this phone. Go back to the test page.');
    document.title = 'probe: .local OK';
    return report('local');
  }

  function item6Background() {
    if (!cfg.urls.local) {
      set('6.local', 'SKIP', 'The computer\'s host name is not a valid DNS label; no .local URL.');
      return Promise.resolve();
    }
    var link = $('locallink');
    link.href = cfg.urls.local + '/local#dev=' + (cfg.device || '');
    var cur = items['6.local'];
    return fetch(cfg.urls.local + '/ping', { mode: 'no-cors', cache: 'no-store' }).then(function () { return true; }, function () { return false; })
      .then(function (ok) {
        extra.localFetch = ok;
        if (!cur || cur.status === 'WAIT' || cur.status === 'INFO') {
          set('6.local', ok ? 'PASS' : 'WAIT', ok ? 'Background fetch to ' + cfg.urls.local + ' succeeded (resolves, certificate trusted).' : 'Tap "Open the .local page" below (a background fetch failed: unresolved, or the certificate is not trusted for that name yet).', { loaded: ok, viaFetch: true });
        }
      });
  }

  function refreshLocal() {
    if (!cfg.device) { return; }
    api('/api/whoami').then(function (w) {
      var cur = items['6.local'];
      if (w.localSeen && (!cur || cur.status !== 'PASS')) {
        set('6.local', 'PASS', 'The .local page loaded and reported in at ' + w.localSeen + '.', { loaded: true });
        report('local');
      }
    });
  }

  // ---- item 8: swipes ---------------------------------------------------------

  var swipe = { left: { start: 0, done: 0, cancel: 0 }, middle: { start: 0, done: 0, cancel: 0 }, right: { start: 0, done: 0, cancel: 0 }, pointerdown: 0, edgePx: 24 };

  function bindSwipe() {
    var el = $('swipe');
    var cur = null;
    function zone(x) { return x < swipe.edgePx ? 'left' : x > document.documentElement.clientWidth - swipe.edgePx ? 'right' : 'middle'; }
    el.addEventListener('touchstart', function (e) {
      var t = e.changedTouches[0];
      cur = { z: zone(t.clientX), x: t.clientX, x0: t.clientX };
      swipe[cur.z].start++;
      showSwipe();
    }, { passive: true });
    el.addEventListener('touchmove', function (e) { if (cur) { cur.x = e.changedTouches[0].clientX; } }, { passive: true });
    el.addEventListener('touchend', function (e) {
      if (!cur) { return; }
      var dx = e.changedTouches[0].clientX - cur.x0;
      if (Math.abs(dx) > 40) { swipe[cur.z].done++; }
      cur = null;
      showSwipe();
    });
    el.addEventListener('touchcancel', function () { if (cur) { swipe[cur.z].cancel++; cur = null; showSwipe(); } });
    if (window.PointerEvent) { el.addEventListener('pointerdown', function () { swipe.pointerdown++; showSwipe(); }); }
    $('swipesend').addEventListener('click', function () { item8(); report('swipe'); });
    showSwipe();
  }

  function showSwipe() {
    function z(n) { var s = swipe[n]; return n + ': ' + s.done + '/' + s.start + ' completed' + (s.cancel ? ', ' + s.cancel + ' cancelled' : ''); }
    $('swipestat').textContent = z('left') + '\n' + z('middle') + '\n' + z('right') + '\npointerdown: ' + swipe.pointerdown + '  (edge = within ' + swipe.edgePx + ' px)';
  }

  function item8() {
    var tried = swipe.left.start + swipe.middle.start + swipe.right.start;
    var st = 'WAIT';
    if (tried) { st = swipe.middle.done && swipe.left.done && swipe.right.done ? 'PASS' : 'INFO'; }
    var d = 'left edge ' + swipe.left.done + '/' + swipe.left.start + ', middle ' + swipe.middle.done + '/' + swipe.middle.start + ', right edge ' + swipe.right.done + '/' + swipe.right.start +
      (st === 'INFO' ? ' — edge swipes lost to the system?' : '') + (tried ? '' : ' — not tried yet (scroll down to the grey box)');
    set('8.swipe', st, d, swipe);
  }

  // ---- item 9: cache -----------------------------------------------------------

  function item9() {
    var key = 'c' + run;
    var u = '/m/thumb.jpg?key=' + key + '&run=' + run;
    var dev = (cfg.device || 'nodev').replace(/[^A-Za-z0-9_-]/g, '');
    var vkey = 'v' + dev;
    var before = null;
    return fetch(u, { credentials: 'same-origin' })
      .then(function (r) { return r.blob(); })
      .then(function () { return sleep(300); })
      .then(function () { return fetch(u, { credentials: 'same-origin' }); })
      .then(function (r) { return r.blob(); })
      .then(function () { return api('/api/cache?key=' + key); })
      .then(function (c) {
        // Across visits: an <img> of a per-device URL, counted before and after.
        return api('/api/cache?key=' + vkey).then(function (b) {
          before = b;
          return new Promise(function (res) {
            var im = new Image();
            im.onload = im.onerror = function () { res(); };
            im.src = '/m/thumb.jpg?key=' + vkey; // the same URL on every visit
          });
        }).then(function () { return api('/api/cache?key=' + vkey); }).then(function (after) {
          var within = c.full === 1 && c.notModified === 0 ? 'cached' : c.full === 1 ? 'revalidated (304)' : 'NOT cached (' + c.full + ' full responses)';
          var first = before.full + before.notModified === 0;
          var across = first ? 'first visit (check again on a later visit)' :
            after.full === before.full && after.notModified === before.notModified ? 'cached' :
            after.notModified > before.notModified ? 'revalidated (304)' : 'NOT cached';
          var st = within === 'cached' ? (first || across === 'cached' ? 'PASS' : 'INFO') : within.indexOf('revalidated') === 0 ? 'INFO' : 'FAIL';
          set('9.cache', st, 'second fetch in this page: ' + within + '\n<img> across visits: ' + across, { within: c, before: before, after: after });
        });
      }, function (e) { set('9.cache', 'FAIL', 'fetch failed: ' + e); });
  }

  // ---- item 10: the constraint port -----------------------------------------------

  function outsidePage() {
    $('items').hidden = true;
    banner('This is the constraint-test certificate (it also names constraint-test.example.com). If you installed the CA, a phone that enforces name constraints must have warned before showing this page.');
    var p = $('later');
    p.hidden = true;
    Array.prototype.forEach.call(document.querySelectorAll('section.panel'), function (s) {
      if (s.querySelector('[data-answer="10.ca-constraints"]') === null && s.querySelector('#send') === null) { s.hidden = true; }
    });
    var d = lsGet('device');
    cfg.device = cfg.device || d || '';
    bindAnswers();
    $('send').addEventListener('click', function () { report('constraint-page'); });
    report('constraint-page');
  }

  function item10(d10) {
    var st = 'WAIT';
    if (d10.installedTrusted === false) { st = 'INFO'; }
    if (d10.installedTrusted !== false && d10.outsideWarned === true) { st = 'PASS'; } // the steps install the CA first
    if (d10.outsideWarned === false) { st = 'FAIL'; }
    var txt = d10.installedTrusted === undefined && d10.outsideWarned === undefined ? 'Optional; see the steps below.' :
      'CA trusted after install: ' + (d10.installedTrusted === undefined ? '?' : d10.installedTrusted ? 'yes' : 'no') +
      '; constraint page warned: ' + (d10.outsideWarned === undefined ? '?' : d10.outsideWarned ? 'yes (constraints enforced)' : 'NO (constraints ignored)');
    if (d10.outsideFetch !== undefined) {
      txt += '\nbackground fetch to the constraint port: ' + (d10.outsideFetch ? 'succeeded — that certificate is accepted here (constraints ignored, or certificate errors ignored)' : 'failed (expected)');
    }
    set('10.ca-constraints', st, txt, d10);
  }

  function item10Background() {
    $('outsidelink').href = cfg.urls.outside + '/constraint';
    return fetch(cfg.urls.outside + '/ping', { mode: 'no-cors', cache: 'no-store' }).then(function () { return true; }, function () { return false; })
      .then(function (ok) {
        var d10 = (items['10.ca-constraints'] && items['10.ca-constraints'].data) || {};
        d10.outsideFetch = ok;
        item10(d10);
      });
  }

  // ---- main ----------------------------------------------------------------------

  function who() {
    $('who').textContent = (cfg.device ? 'Paired as ' + cfg.device : 'Not paired') + ' · ' + location.host + ' · ' + (cfg.proto || '') +
      ' · cookie on this load: ' + (cfg.cookieSent ? 'yes' : 'no');
  }

  function checklist() {
    $('dl').href = '/m/original.jpg?download=1&run=' + run;
    $('dl').addEventListener('click', function () { setTimeout(function () { checkDownload(); }, 1500); });
    banner('Running the checklist… (about 20 s)');
    laterPanel();
    var fetchOK = false;
    return api('/api/whoami')
      .then(function (w) { fetchOK = !!(w.cookieSent && w.device && w.device === cfg.device); item2(fetchOK); })
      .then(function () { return Promise.all([imageTest(), videoTests().then(item4), item5(), item9(), item6Background(), item10Background()]); })
      .then(function () { item4(); return sleep(500).then(obsFor); })
      .then(item3)
      .then(function () {
        if (!items['8.swipe'] || items['8.swipe'].status === 'WAIT') { item8(); }
        banner('');
        return report('auto');
      }, function (e) {
        errors.push('checklist: ' + e);
        banner('The checklist stopped: ' + e);
        return report('auto-error');
      });
  }

  function start() {
    who();
    render();
    bindCodeForm();
    var outsidePort = (cfg.urls.outside.match(/:(\d+)$/) || [])[1];
    if (location.port === outsidePort) { outsidePage(); return; }
    bindAnswers();
    bindSwipe();
    $('send').addEventListener('click', function () {
      if (swipe.left.start + swipe.middle.start + swipe.right.start) { item8(); }
      report('manual');
    });
    $('outsidelink').href = cfg.urls.outside + '/constraint';
    document.addEventListener('visibilitychange', function () { if (!document.hidden) { refreshLocal(); } });
    window.addEventListener('pageshow', refreshLocal);

    if (/\.local$/.test(location.hostname) && location.pathname === '/local') { localPage(); return; }
    if (location.pathname === '/pair') { pairPage(); return; }
    if (cookieSteps()) { return; }
    if (!cfg.device) {
      var had = lsGet('device');
      if (had) {
        var d2 = (items['2.cookie'] && items['2.cookie'].data) || {};
        d2.thisLoad = false;
        set('2.cookie', 'FAIL', 'This browser paired as ' + had + ' ' + hoursSince(lsGet('pairedAt')) + ' h ago, but this load carried no valid cookie.', d2);
        laterPanel();
        showPairForm('The pairing cookie is gone. Answer the questions above, then pair again with the code to continue.');
        report('cookie-lost');
      } else {
        showPairForm('Scan the QR code from the terminal, or type the code shown there.');
      }
      return;
    }
    checklist();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
})();
