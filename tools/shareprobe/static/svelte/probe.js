var qr = Object.defineProperty;
var Dn = (e) => {
  throw TypeError(e);
};
var jr = (e, t, n) => t in e ? qr(e, t, { enumerable: !0, configurable: !0, writable: !0, value: n }) : e[t] = n;
var V = (e, t, n) => jr(e, typeof t != "symbol" ? t + "" : t, n), Jt = (e, t, n) => t.has(e) || Dn("Cannot " + n);
var l = (e, t, n) => (Jt(e, t, "read from private field"), n ? n.call(e) : t.get(e)), g = (e, t, n) => t.has(e) ? Dn("Cannot add the same private member more than once") : t instanceof WeakSet ? t.add(e) : t.set(e, n), _ = (e, t, n, r) => (Jt(e, t, "write to private field"), r ? r.call(e, n) : t.set(e, n), n), m = (e, t, n) => (Jt(e, t, "access private method"), n);
var Zn = Array.isArray, zr = Array.prototype.indexOf, qt = Array.prototype.includes, Gt = Array.from, Jn = Object.defineProperty, yt = Object.getOwnPropertyDescriptor, Br = Object.prototype, Vr = Array.prototype, Ur = Object.getPrototypeOf, Pn = Object.isExtensible;
const Yr = () => {
};
function Hr(e) {
  for (var t = 0; t < e.length; t++)
    e[t]();
}
function Qn() {
  var e, t, n = new Promise((r, i) => {
    e = r, t = i;
  });
  return { promise: n, resolve: e, reject: t };
}
const F = 2, St = 4, Kt = 8, Xn = 1 << 24, ie = 16, X = 32, ke = 64, ln = 128, En = 256, fe = 512, N = 1024, R = 2048, Q = 4096, I = 8192, q = 16384, at = 32768, fn = 1 << 25, lt = 65536, jt = 1 << 17, $r = 1 << 18, ct = 1 << 19, Gr = 1 << 20, Ee = 1 << 25, zt = 1 << 21, Qe = 1 << 22, Oe = 1 << 23, Qt = /* @__PURE__ */ Symbol("$state"), er = /* @__PURE__ */ Symbol("component"), Kr = /* @__PURE__ */ Symbol("attributes"), un = /* @__PURE__ */ Symbol("class"), Wr = /* @__PURE__ */ Symbol("style"), vt = /* @__PURE__ */ Symbol("text"), Mt = new class extends Error {
  constructor() {
    super(...arguments);
    V(this, "name", "StaleReactionError");
    V(this, "message", "The reaction that called `getAbortSignal()` was re-run or destroyed");
  }
}(), Zr = 1, Jr = 2, Qr = 16, Xr = 2, M = /* @__PURE__ */ Symbol("uninitialized");
function ei() {
  console.warn("https://svelte.dev/e/derived_inert");
}
function ti() {
  console.warn("https://svelte.dev/e/svelte_boundary_reset_noop");
}
function tr(e) {
  return e === this.v;
}
function ni(e, t) {
  return e != e ? t == t : e !== t || e !== null && typeof e == "object" || typeof e == "function";
}
function nr(e) {
  return !ni(e, this.v);
}
function ri() {
  throw new Error("https://svelte.dev/e/async_derived_orphan");
}
function ii(e, t, n) {
  throw new Error("https://svelte.dev/e/each_key_duplicate");
}
function si(e) {
  throw new Error("https://svelte.dev/e/effect_in_teardown");
}
function li() {
  throw new Error("https://svelte.dev/e/effect_in_unowned_derived");
}
function fi(e) {
  throw new Error("https://svelte.dev/e/effect_orphan");
}
function ui() {
  throw new Error("https://svelte.dev/e/effect_update_depth_exceeded");
}
function oi() {
  throw new Error("https://svelte.dev/e/state_descriptors_fixed");
}
function ai() {
  throw new Error("https://svelte.dev/e/state_prototype_fixed");
}
function ci() {
  throw new Error("https://svelte.dev/e/state_unsafe_mutation");
}
function hi() {
  throw new Error("https://svelte.dev/e/svelte_boundary_reset_onerror");
}
let j = null;
function ft(e) {
  j = e;
}
function rr(e, t = !1, n) {
  j = {
    p: j,
    i: !1,
    c: null,
    e: null,
    s: e,
    x: null,
    r: (
      /** @type {Effect} */
      T
    ),
    l: null
  };
}
function ir(e) {
  var t = (
    /** @type {ComponentContext} */
    j
  ), n = t.e;
  if (n !== null) {
    t.e = null;
    for (var r of n)
      kr(r);
  }
  return e !== void 0 && (t.x = e), t.i = !0, j = t.p, sr(e);
}
function sr(e = {}) {
  return Jn(e, er, { value: !0 }), e;
}
function lr() {
  return !0;
}
let De = [];
function fr() {
  var e = De;
  De = [], Hr(e);
}
function Pe(e) {
  if (De.length === 0 && !bt) {
    var t = De;
    queueMicrotask(() => {
      t === De && fr();
    });
  }
  De.push(e);
}
function di() {
  for (; De.length > 0; )
    fr();
}
const vi = -7169;
function C(e, t) {
  e.f = e.f & vi | t;
}
function kn(e) {
  (e.f & fe) !== 0 || e.deps === null ? C(e, N) : C(e, Q);
}
function ur(e, t, n) {
  (e.f & R) !== 0 ? t.add(e) : (e.f & Q) !== 0 && n.add(e), C(e, N);
}
function Wt(e) {
  var t = y, n = T;
  ee(null), _e(null);
  try {
    return e();
  } finally {
    ee(t), _e(n);
  }
}
function _i(e, t, n, r) {
  const i = Sn;
  var s = e.filter((d) => !d.settled), u = t.map(i);
  if (n.length === 0 && s.length === 0) {
    r(u);
    return;
  }
  var f = (
    /** @type {Effect} */
    T
  ), a = pi(), o = s.length === 1 ? s[0].promise : s.length > 1 ? Promise.all(s.map((d) => d.promise)) : null;
  function h(d) {
    if ((f.f & q) === 0) {
      a();
      try {
        r([...u, ...d]);
      } catch (c) {
        de(c, f);
      }
      Bt();
    }
  }
  var v = or();
  if (n.length === 0) {
    o.then(() => h([])).finally(v);
    return;
  }
  function p() {
    Promise.all(n.map((d) => /* @__PURE__ */ gi(d))).then(h).catch((d) => de(d, f)).finally(v);
  }
  o ? o.then(() => {
    a(), p(), Bt();
  }) : p();
}
function pi() {
  var e = (
    /** @type {Effect} */
    T
  ), t = y, n = j, r = (
    /** @type {Batch} */
    E
  );
  return function(s = !0) {
    _e(e), ee(t), ft(n), s && (e.f & q) === 0 && (r?.activate(), r?.apply());
  };
}
function Bt(e = !0) {
  _e(null), ee(null), ft(null), e && E?.deactivate();
}
function or() {
  var e = (
    /** @type {Effect} */
    T
  ), t = e.b, n = (
    /** @type {Batch} */
    E
  ), r = !!t?.is_rendered();
  return t?.update_pending_count(1, n), n.increment(r, e), () => {
    t?.update_pending_count(-1, n), n.decrement(r, e);
  };
}
// @__NO_SIDE_EFFECTS__
function Sn(e) {
  var t = F | R;
  return T !== null && (T.f |= ct), {
    ctx: j,
    deps: null,
    effects: null,
    equals: tr,
    f: t,
    fn: e,
    reactions: null,
    rv: 0,
    v: (
      /** @type {V} */
      M
    ),
    wv: 0,
    parent: T,
    ac: null
  };
}
const _t = /* @__PURE__ */ Symbol("obsolete");
// @__NO_SIDE_EFFECTS__
function gi(e, t, n) {
  let r = (
    /** @type {Effect | null} */
    T
  );
  r === null && ri();
  var i = (
    /** @type {Promise<V>} */
    /** @type {unknown} */
    void 0
  ), s = Ge(
    /** @type {V} */
    M
  ), u = !y, f = /* @__PURE__ */ new Set();
  return Ii(() => {
    var a = (
      /** @type {Effect} */
      T
    ), o = Qn();
    i = o.promise;
    try {
      Promise.resolve(e()).then(o.resolve, (d) => {
        d !== Mt && o.reject(d);
      }).finally(Bt);
    } catch (d) {
      o.reject(d), Bt();
    }
    var h = (
      /** @type {Batch} */
      E
    );
    if (u) {
      if ((a.f & at) !== 0)
        var v = or();
      if (
        // boundary can be null if the async derived is inside an $effect.root not connected to the component render tree
        r.b?.is_rendered()
      )
        h.async_deriveds.get(a)?.reject(_t);
      else
        for (const d of f.values())
          d.reject(_t);
      f.add(o), h.async_deriveds.set(a, o);
    }
    const p = (d, c = void 0) => {
      v?.(), f.delete(o), c !== _t && (h.activate(), c ? (s.f |= Oe, ut(s, c)) : ((s.f & Oe) !== 0 && (s.f ^= Oe), ut(s, d)), h.deactivate());
    };
    o.promise.then(p, (d) => p(null, d || "unknown"));
  }), Fi(() => {
    for (const a of f)
      a.reject(_t);
  }), new Promise((a) => {
    function o(h) {
      function v() {
        h === i ? a(s) : o(i);
      }
      h.then(v, v);
    }
    o(i);
  });
}
// @__NO_SIDE_EFFECTS__
function In(e) {
  const t = /* @__PURE__ */ Sn(e);
  return Cr(t), t;
}
// @__NO_SIDE_EFFECTS__
function wi(e) {
  const t = /* @__PURE__ */ Sn(e);
  return t.equals = nr, t;
}
function mi(e) {
  var t = e.effects;
  if (t !== null) {
    e.effects = null;
    for (var n = 0; n < t.length; n += 1)
      z(
        /** @type {Effect} */
        t[n]
      );
  }
}
function Tn(e) {
  var t, n = T, r = e.parent;
  if (!Re && r !== null && e.v !== M && // if it was never evaluated before, it's guaranteed to fail downstream, so we try to execute instead
  (r.f & (q | I)) !== 0)
    return ei(), e.v;
  _e(r);
  try {
    mi(e), t = Nr(e);
  } finally {
    _e(n);
  }
  return t;
}
function ar(e) {
  var t = Tn(e);
  if (!e.equals(t) && (e.wv = Rr(), (!E?.is_fork || e.deps === null) && (E !== null ? (E.capture(e, t, !0), on?.capture(e, t, !0)) : e.v = t, e.deps === null))) {
    C(e, N);
    return;
  }
  Re || (se !== null ? (On() || E?.is_fork) && se.set(e, t) : kn(e));
}
function yi(e) {
  if (e.effects !== null)
    for (const t of e.effects)
      (t.teardown || t.ac) && (t.teardown?.(), t.ac !== null && Wt(() => {
        t.ac.abort(Mt), t.ac = null;
      }), t.fn !== null && (t.teardown = Yr), Tt(t, 0), Mn(t));
}
function cr(e) {
  if (e.effects !== null)
    for (const t of e.effects)
      t.teardown && t.fn !== null && ot(t);
}
let Xt = null, Ke = null, E = null, on = null, se = null, an = null, bt = !1, en = !1, Et = null, Pt = null;
var Ln = 0;
let bi = 1;
var Xe, Ae, qe, et, tt, nt, me, rt, U, xt, ye, ne, ae, it, je, k, cn, hn, pt, dn, hr, dr, We, Ei, gt;
const Yt = class Yt {
  constructor() {
    g(this, k);
    V(this, "id", bi++);
    /** True as soon as `#process` was called */
    g(this, Xe, !1);
    V(this, "linked", !0);
    /** @type {Batch | null} */
    g(this, Ae, null);
    /** @type {Batch | null} */
    g(this, qe, null);
    /** @type {Map<Effect, ReturnType<typeof deferred<any>>>} */
    V(this, "async_deriveds", /* @__PURE__ */ new Map());
    /**
     * The current values of any signals that are updated in this batch.
     * Tuple format: [value, is_derived] (note: is_derived is false for deriveds, too, if they were overridden via assignment)
     * They keys of this map are identical to `this.#previous`
     * @type {Map<Value, [any, boolean]>}
     */
    V(this, "current", /* @__PURE__ */ new Map());
    /**
     * The values of any signals (sources and deriveds) that are updated in this batch _before_ those updates took place.
     * They keys of this map are identical to `this.#current`
     * @type {Map<Value, any>}
     */
    V(this, "previous", /* @__PURE__ */ new Map());
    /**
     * When the batch is committed (and the DOM is updated), we need to remove old branches
     * and append new ones by calling the functions added inside (if/each/key/etc) blocks
     * @type {Set<(batch: Batch) => void>}
     */
    g(this, et, /* @__PURE__ */ new Set());
    /**
     * If a fork is discarded, we need to destroy any effects that are no longer needed
     * @type {Set<(batch: Batch) => void>}
     */
    g(this, tt, /* @__PURE__ */ new Set());
    /**
     * The number of async effects that are currently in flight
     */
    g(this, nt, 0);
    /**
     * Async effects that are currently in flight, _not_ inside a pending boundary
     * @type {Map<Effect, number>}
     */
    g(this, me, /* @__PURE__ */ new Map());
    /**
     * A deferred that resolves when the batch is committed, used with `settled()`
     * TODO replace with Promise.withResolvers once supported widely enough
     * @type {{ promise: Promise<void>, resolve: (value?: any) => void, reject: (reason: unknown) => void } | null}
     */
    g(this, rt, null);
    /**
     * Effects that were scheduled in this batch but not yet 'resolved' into the
     * root effects that need to be flushed. Resolving — the upwards traversal that
     * marks the path to each effect on the shared effect tree (see #resolve) — is
     * deferred until the batch is processed, so that the markers are created and
     * consumed within a single traversal. Scheduling into other batches (which can
     * happen concurrently, e.g. while a batch is committed) can therefore never
     * observe (and be confused by) this batch's markers.
     * May contain duplicates — deduplication happens during resolving
     * @type {Effect[]}
     */
    g(this, U, []);
    /**
     * Effects created while this batch was active.
     * @type {Effect[]}
     */
    g(this, xt, []);
    /**
     * Deferred effects (which run after async work has completed) that are DIRTY
     * @type {Set<Effect>}
     */
    g(this, ye, /* @__PURE__ */ new Set());
    /**
     * Deferred effects that are MAYBE_DIRTY
     * @type {Set<Effect>}
     */
    g(this, ne, /* @__PURE__ */ new Set());
    /**
     * A map of branches that still exist, but will be destroyed when this batch
     * is committed — we skip over these during `process`.
     * The value contains child effects that were dirty/maybe_dirty before being reset,
     * so they can be rescheduled if the branch survives.
     * @type {Map<Effect, { d: Effect[], m: Effect[] }>}
     */
    g(this, ae, /* @__PURE__ */ new Map());
    /**
     * Inverse of #skipped_branches which we need to tell prior batches to unskip them when committing
     * @type {Set<Effect>}
     */
    g(this, it, /* @__PURE__ */ new Set());
    V(this, "is_fork", !1);
    g(this, je, !1);
    Ke === null ? Xt = Ke = this : (_(Ke, qe, this), _(this, Ae, Ke)), Ke = this;
  }
  /**
   * Add an effect to the #skipped_branches map and reset its children
   * @param {Effect} effect
   */
  skip_effect(t) {
    l(this, ae).has(t) || l(this, ae).set(t, { d: [], m: [] }), l(this, it).delete(t);
  }
  /**
   * Remove an effect from the #skipped_branches map and reschedule
   * any tracked dirty/maybe_dirty child effects
   * @param {Effect} effect
   * @param {(e: Effect) => void} callback
   */
  unskip_effect(t, n = (r) => this.schedule(r)) {
    var r = l(this, ae).get(t);
    if (r) {
      l(this, ae).delete(t);
      for (var i of r.d)
        C(i, R), n(i);
      for (i of r.m)
        C(i, Q), n(i);
    }
    l(this, it).add(t);
  }
  /**
   * Associate a change to a given source with the current
   * batch, noting its previous and current values
   * @param {Value} source
   * @param {any} value
   * @param {boolean} [is_derived]
   */
  capture(t, n, r = !1) {
    t.v !== M && !this.previous.has(t) && this.previous.set(t, t.v), (t.f & Oe) === 0 && (this.current.set(t, [n, r]), se?.set(t, n)), this.is_fork || (t.v = n);
  }
  activate() {
    E = this;
  }
  deactivate() {
    E = null, se = null;
  }
  flush() {
    try {
      en = !0, E = this, m(this, k, pt).call(this);
    } finally {
      Ln = 0, an = null, Et = null, Pt = null, en = !1, E = null, se = null, ve.clear();
    }
  }
  discard() {
    for (const t of l(this, tt)) t(this);
    l(this, tt).clear();
    for (const t of this.async_deriveds.values())
      t.reject(_t);
    m(this, k, gt).call(this), l(this, rt)?.resolve();
  }
  /**
   * @param {Effect} effect
   */
  register_created_effect(t) {
    l(this, xt).push(t);
  }
  /**
   * @param {boolean} blocking
   * @param {Effect} effect
   */
  increment(t, n) {
    if (_(this, nt, l(this, nt) + 1), t) {
      let r = l(this, me).get(n) ?? 0;
      l(this, me).set(n, r + 1);
    }
  }
  /**
   * @param {boolean} blocking
   * @param {Effect} effect
   */
  decrement(t, n) {
    if (_(this, nt, l(this, nt) - 1), t) {
      let r = l(this, me).get(n) ?? 0;
      r === 1 ? l(this, me).delete(n) : l(this, me).set(n, r - 1);
    }
    l(this, je) || (_(this, je, !0), Pe(() => {
      _(this, je, !1), this.linked && this.flush();
    }));
  }
  /**
   * @param {Set<Effect>} dirty_effects
   * @param {Set<Effect>} maybe_dirty_effects
   */
  transfer_effects(t, n) {
    for (const r of t)
      l(this, ye).add(r);
    for (const r of n)
      l(this, ne).add(r);
    t.clear(), n.clear();
  }
  /** @param {(batch: Batch) => void} fn */
  oncommit(t) {
    l(this, et).add(t);
  }
  /** @param {(batch: Batch) => void} fn */
  ondiscard(t) {
    l(this, tt).add(t);
  }
  settled() {
    return (l(this, rt) ?? _(this, rt, Qn())).promise;
  }
  static ensure() {
    if (E === null) {
      const t = E = new Yt();
      !en && !bt && Pe(() => {
        l(t, Xe) || t.flush();
      });
    }
    return E;
  }
  apply() {
    {
      se = null;
      return;
    }
  }
  /**
   *
   * @param {Effect} effect
   */
  schedule(t) {
    if (an = t, t.b?.is_pending && (t.f & (St | Kt | Xn)) !== 0 && (t.f & at) === 0) {
      t.b.defer_effect(t);
      return;
    }
    l(this, U).push(t);
  }
};
Xe = new WeakMap(), Ae = new WeakMap(), qe = new WeakMap(), et = new WeakMap(), tt = new WeakMap(), nt = new WeakMap(), me = new WeakMap(), rt = new WeakMap(), U = new WeakMap(), xt = new WeakMap(), ye = new WeakMap(), ne = new WeakMap(), ae = new WeakMap(), it = new WeakMap(), je = new WeakMap(), k = new WeakSet(), cn = function() {
  if (this.is_fork) return !0;
  for (const r of l(this, me).keys()) {
    for (var t = r, n = !1; t.parent !== null; ) {
      if (l(this, ae).has(t)) {
        n = !0;
        break;
      }
      t = t.parent;
    }
    if (!n)
      return !0;
  }
  return !1;
}, /**
 * Convert the effects that were scheduled in this batch into the root effects
 * that need to be traversed, marking the path to each effect (by clearing the
 * `CLEAN` flag on ancestor branches) so that the traversal can find them.
 * This happens right before traversal rather than at scheduling time, so that
 * the markers left on the (shared) effect tree are created and consumed within
 * a single traversal — scheduling into other batches can never observe them
 * @returns {Effect[]}
 */
hn = function() {
  var t = [];
  for (const s of l(this, U))
    if (!((s.f & q) !== 0 || (s.f & (R | Q)) === 0)) {
      for (var n = s, r = !1; n.parent !== null; ) {
        n = n.parent;
        var i = n.f;
        if ((i & (ke | X)) !== 0) {
          if ((i & N) === 0) {
            r = !0;
            break;
          }
          n.f ^= N;
        }
      }
      r || t.push(n);
    }
  return _(this, U, []), t;
}, pt = function() {
  var f, a, o;
  _(this, Xe, !0);
  for (const h of l(this, ye))
    l(this, ne).delete(h), C(h, R), this.schedule(h);
  for (const h of l(this, ne))
    C(h, Q), this.schedule(h);
  this.apply();
  for (var t = Et = [], n = [], r = Pt = []; l(this, U).length > 0; ) {
    Ln++ > 1e3 && (m(this, k, gt).call(this), ki());
    for (const h of m(this, k, hn).call(this))
      try {
        m(this, k, dn).call(this, h, t, n);
      } catch (v) {
        throw pr(h), m(this, k, cn).call(this) || this.discard(), v;
      }
  }
  if (E = null, r.length > 0) {
    var i = Yt.ensure();
    for (const h of r)
      i.schedule(h);
  }
  if (Et = null, Pt = null, m(this, k, cn).call(this)) {
    m(this, k, We).call(this, n), m(this, k, We).call(this, t);
    for (const [h, v] of l(this, ae))
      _r(h, v);
    r.length > 0 && /** @type {unknown} */
    m(f = E, k, pt).call(f);
    return;
  }
  const s = m(this, k, hr).call(this);
  if (s) {
    m(this, k, We).call(this, n), m(this, k, We).call(this, t), m(a = s, k, dr).call(a, this);
    return;
  }
  l(this, ye).clear(), l(this, ne).clear();
  for (const h of l(this, et)) h(this);
  l(this, et).clear(), on = this, jn(n), jn(t), on = null, l(this, rt)?.resolve();
  var u = (
    /** @type {Batch | null} */
    /** @type {unknown} */
    E
  );
  if (l(this, nt) === 0 && (l(this, U).length === 0 || u !== null) && m(this, k, gt).call(this), l(this, U).length > 0)
    if (u !== null) {
      for (const h of l(this, U))
        l(u, U).push(h);
      _(this, U, []);
    } else
      u = this;
  u !== null && (ve.clear(), m(o = u, k, pt).call(o));
}, /**
 * Traverse the effect tree, executing effects or stashing
 * them for later execution as appropriate
 * @param {Effect} root
 * @param {Effect[]} effects
 * @param {Effect[]} render_effects
 */
dn = function(t, n, r) {
  t.f ^= N;
  for (var i = t.first; i !== null; ) {
    var s = i.f, u = (s & (X | ke)) !== 0, f = u && (s & N) !== 0, a = f || (s & I) !== 0 || l(this, ae).has(i);
    if (!a && i.fn !== null) {
      u ? i.f ^= N : (s & St) !== 0 ? n.push(i) : Nt(i) && ((s & ie) !== 0 && l(this, ne).add(i), ot(i));
      var o = i.first;
      if (o !== null) {
        i = o;
        continue;
      }
    }
    for (; i !== null; ) {
      var h = i.next;
      if (h !== null) {
        i = h;
        break;
      }
      i = i.parent;
    }
  }
}, hr = function() {
  for (var t = l(this, Ae); t !== null; ) {
    if (!t.is_fork) {
      for (const [n, [, r]] of this.current)
        if (t.current.has(n) && !r)
          return t;
    }
    t = l(t, Ae);
  }
  return null;
}, /**
 * @param {Batch} batch
 */
dr = function(t) {
  var r;
  for (const [i, s] of t.current)
    !this.previous.has(i) && t.previous.has(i) && this.previous.set(i, t.previous.get(i)), this.current.set(i, s);
  for (const [i, s] of t.async_deriveds) {
    const u = this.async_deriveds.get(i);
    u && s.promise.then(u.resolve).catch(u.reject);
  }
  t.async_deriveds.clear(), this.transfer_effects(l(t, ye), l(t, ne));
  const n = (i) => {
    var s = i.reactions;
    if (s !== null && !((i.f & F) !== 0 && (i.f & (R | Q)) === 0))
      for (const a of s) {
        var u = a.f;
        if ((u & F) !== 0)
          n(
            /** @type {Derived} */
            a
          );
        else {
          var f = (
            /** @type {Effect} */
            a
          );
          u & (Qe | ie) && !this.async_deriveds.has(f) && (l(this, ne).delete(f), C(f, R), this.schedule(f));
        }
      }
  };
  for (const i of this.current.keys())
    n(i);
  this.oncommit(() => t.discard()), m(r = t, k, gt).call(r), E = this, m(this, k, pt).call(this);
}, /**
 * @param {Effect[]} effects
 */
We = function(t) {
  for (var n = 0; n < t.length; n += 1)
    ur(t[n], l(this, ye), l(this, ne));
}, Ei = function() {
  var v, p;
  for (let d = Xt; d !== null; d = l(d, qe)) {
    var t = d.id < this.id, n = [];
    for (const [c, [w, b]] of this.current) {
      if (d.current.has(c)) {
        var r = (
          /** @type {[any, boolean]} */
          d.current.get(c)[0]
        );
        if (t && w !== r)
          d.current.set(c, [w, b]);
        else
          continue;
      }
      n.push(c);
    }
    if (t)
      for (const [c, w] of this.async_deriveds) {
        const b = d.async_deriveds.get(c);
        b && w.promise.then(b.resolve).catch(b.reject);
      }
    var i = [...d.current.keys()].filter(
      (c) => !/** @type {[any, boolean]} */
      d.current.get(c)[1]
    );
    if (!(!l(d, Xe) || i.length === 0)) {
      var s = i.filter((c) => !this.current.has(c));
      if (s.length === 0)
        t && d.discard();
      else if (n.length > 0) {
        if (t)
          for (const c of l(this, it))
            d.unskip_effect(c, (w) => {
              var b;
              (w.f & (ie | Qe)) !== 0 ? d.schedule(w) : m(b = d, k, We).call(b, [w]);
            });
        d.activate();
        var u = /* @__PURE__ */ new Set(), f = /* @__PURE__ */ new Map();
        for (var a of n)
          vr(a, s, u, f);
        f = /* @__PURE__ */ new Map();
        var o = [...d.current].filter(([c, w]) => {
          const b = this.current.get(c);
          return b ? b[0] !== w[0] || b[1] !== w[1] : !0;
        }).map(([c]) => c);
        if (o.length > 0)
          for (const c of l(this, xt))
            (c.f & (q | I | jt)) === 0 && xn(c, o, f) && ((c.f & (Qe | ie)) !== 0 ? (C(c, R), d.schedule(c)) : l(d, ye).add(c));
        if (l(d, U).length > 0 && !l(d, je)) {
          d.apply();
          for (var h of m(v = d, k, hn).call(v))
            m(p = d, k, dn).call(p, h, [], []);
        }
        d.deactivate();
      }
    }
  }
}, gt = function() {
  if (this.linked) {
    var t = l(this, Ae), n = l(this, qe);
    t === null ? Xt = n : _(t, qe, n), n === null ? Ke = t : _(n, Ae, t), this.linked = !1;
  }
};
let $e = Yt;
function qn(e) {
  var t = bt;
  bt = !0;
  try {
    for (var n; ; ) {
      if (di(), E === null)
        return (
          /** @type {T} */
          n
        );
      E.flush();
    }
  } finally {
    bt = t;
  }
}
function ki() {
  try {
    ui();
  } catch (e) {
    de(e, an);
  }
}
let we = null;
function jn(e) {
  var t = e.length;
  if (t !== 0) {
    for (var n = 0; n < t; ) {
      var r = e[n++];
      if ((r.f & (q | I)) === 0 && Nt(r) && (we = /* @__PURE__ */ new Set(), ot(r), r.deps === null && r.first === null && r.nodes === null && r.teardown === null && r.ac === null && Tr(r), we?.size > 0)) {
        ve.clear();
        for (const i of we) {
          if ((i.f & (q | I)) !== 0) continue;
          const s = [i];
          let u = i.parent;
          for (; u !== null; )
            we.has(u) && (we.delete(u), s.push(u)), u = u.parent;
          for (let f = s.length - 1; f >= 0; f--) {
            const a = s[f];
            (a.f & (q | I)) === 0 && ot(a);
          }
        }
        we.clear();
      }
    }
    we = null;
  }
}
function vr(e, t, n, r) {
  if (!n.has(e) && (n.add(e), e.reactions !== null))
    for (const i of e.reactions) {
      const s = i.f;
      (s & F) !== 0 ? vr(
        /** @type {Derived} */
        i,
        t,
        n,
        r
      ) : (s & (Qe | ie)) !== 0 && (s & R) === 0 && xn(i, t, r) && (C(i, R), An(
        /** @type {Effect} */
        i
      ));
    }
}
function xn(e, t, n) {
  const r = n.get(e);
  if (r !== void 0) return r;
  if (e.deps !== null)
    for (const i of e.deps) {
      if (qt.call(t, i))
        return !0;
      if ((i.f & F) !== 0 && xn(
        /** @type {Derived} */
        i,
        t,
        n
      ))
        return n.set(
          /** @type {Derived} */
          i,
          !0
        ), !0;
    }
  return n.set(e, !1), !1;
}
function An(e) {
  E.schedule(e);
}
function _r(e, t) {
  if (!((e.f & X) !== 0 && (e.f & N) !== 0)) {
    (e.f & R) !== 0 ? t.d.push(e) : (e.f & Q) !== 0 && t.m.push(e), C(e, N);
    for (var n = e.first; n !== null; )
      _r(n, t), n = n.next;
  }
}
function pr(e) {
  C(e, N);
  for (var t = e.first; t !== null; )
    pr(t), t = t.next;
}
let Vt = /* @__PURE__ */ new Set();
const ve = /* @__PURE__ */ new Map();
let gr = !1;
function Ge(e, t) {
  var n = {
    f: 0,
    // TODO ideally we could skip this altogether, but it causes type errors
    v: e,
    reactions: null,
    equals: tr,
    rv: 0,
    wv: 0
  };
  return n;
}
// @__NO_SIDE_EFFECTS__
function ge(e, t) {
  const n = Ge(e);
  return Cr(n), n;
}
// @__NO_SIDE_EFFECTS__
function Si(e, t = !1, n = !0) {
  const r = Ge(e);
  return t || (r.equals = nr), r;
}
function xe(e, t, n = !1) {
  y !== null && // since we are untracking the function inside `$inspect.with` we need to add this check
  // to ensure we error if state is set inside an inspect effect
  (!le || (y.f & jt) !== 0) && lr() && (y.f & (F | ie | Qe | jt)) !== 0 && (ue === null || !ue.has(e)) && ci();
  let r = n ? Je(t) : t;
  return ut(e, r, Pt);
}
var Fe = null, vn = 0;
function ut(e, t, n = null) {
  if (!e.equals(t)) {
    Re ? ve.set(e, t) : ve.has(e) || ve.set(e, e.v);
    var r = $e.ensure();
    if (r.capture(e, t), (e.f & F) !== 0) {
      const i = (
        /** @type {Derived} */
        e
      );
      (e.f & R) !== 0 && Tn(i), se === null && kn(i);
    }
    e.wv = Rr(), Fe = null, vn = 0, wr(e, R, n), Fe = null, T !== null && (T.f & N) !== 0 && (T.f & (X | ke)) === 0 && (K === null ? zi([e]) : K.push(e)), !r.is_fork && Vt.size > 0 && !gr && Ti();
  }
  return t;
}
function Ti() {
  gr = !1;
  for (const e of Vt) {
    (e.f & N) !== 0 && C(e, Q);
    let t;
    try {
      t = Nt(e);
    } catch {
      t = !0;
    }
    t && ot(e);
  }
  Vt.clear();
}
function kt(e) {
  xe(e, e.v + 1);
}
function wr(e, t, n) {
  var r = e.reactions;
  if (r !== null) {
    var i = r.length;
    if (vn += i, vn > 1e5 && Fe === null && (Fe = /* @__PURE__ */ new Set()), Fe !== null) {
      if (Fe.has(e)) return;
      Fe.add(e);
    }
    for (var s = 0; s < i; s++) {
      var u = r[s], f = u.f, a = (f & R) === 0;
      if (a && C(u, t), (f & jt) !== 0)
        Vt.add(
          /** @type {Effect} */
          u
        );
      else if ((f & F) !== 0) {
        var o = (
          /** @type {Derived} */
          u
        );
        se?.delete(o), wr(o, Q, n);
      } else if (a) {
        var h = (
          /** @type {Effect} */
          u
        );
        (f & ie) !== 0 && we !== null && we.add(h), n !== null ? n.push(h) : An(h);
      }
    }
  }
}
function Je(e) {
  if (typeof e != "object" || e === null || Qt in e || er in e)
    return e;
  const t = Ur(e);
  if (t !== Br && t !== Vr)
    return e;
  var n = /* @__PURE__ */ new Map(), r = Zn(e), i = /* @__PURE__ */ ge(0), s = He, u = (f) => {
    if (He === s)
      return f();
    var a = y, o = He;
    ee(null), Yn(s);
    var h = f();
    return ee(a), Yn(o), h;
  };
  return r && n.set("length", /* @__PURE__ */ ge(
    /** @type {any[]} */
    e.length
  )), new Proxy(
    /** @type {any} */
    e,
    {
      defineProperty(f, a, o) {
        (!("value" in o) || o.configurable === !1 || o.enumerable === !1 || o.writable === !1) && oi();
        var h = n.get(a);
        return h === void 0 ? u(() => {
          var v = /* @__PURE__ */ ge(o.value);
          return n.set(a, v), v;
        }) : xe(h, o.value, !0), !0;
      },
      deleteProperty(f, a) {
        var o = n.get(a);
        if (o === void 0) {
          if (a in f) {
            const h = u(() => /* @__PURE__ */ ge(M));
            n.set(a, h), kt(i);
          }
        } else
          xe(o, M), kt(i);
        return !0;
      },
      get(f, a, o) {
        if (a === Qt)
          return e;
        var h = n.get(a), v = a in f;
        if (h === void 0 && (!v || yt(f, a)?.writable) && (h = u(() => {
          var d = Je(v ? f[a] : M), c = /* @__PURE__ */ ge(d);
          return c;
        }), n.set(a, h)), h !== void 0) {
          var p = O(h);
          return p === M ? void 0 : p;
        }
        return Reflect.get(f, a, o);
      },
      getOwnPropertyDescriptor(f, a) {
        this.has?.(f, a);
        var o = Reflect.getOwnPropertyDescriptor(f, a), h = n.get(a);
        if (h !== void 0) {
          var v = O(h);
          if (v === M)
            return;
          if (o && "value" in o)
            o.value = v;
          else
            return {
              enumerable: !0,
              configurable: !0,
              value: v,
              writable: !0
            };
        }
        return o;
      },
      has(f, a) {
        if (a === Qt)
          return !0;
        var o = n.get(a), h = o !== void 0 && o.v !== M || Reflect.has(f, a);
        if (o !== void 0 || T !== null && (!h || yt(f, a)?.writable)) {
          o === void 0 && (o = u(() => {
            var p = h ? Je(f[a]) : M, d = /* @__PURE__ */ ge(p);
            return d;
          }), n.set(a, o));
          var v = O(o);
          if (v === M)
            return !1;
        }
        return h;
      },
      set(f, a, o, h) {
        var v = n.get(a), p = a in f;
        if (r && a === "length")
          for (var d = o; d < /** @type {Source<number>} */
          v.v; d += 1) {
            var c = n.get(d + "");
            c !== void 0 ? xe(c, M) : d in f && (c = u(() => /* @__PURE__ */ ge(M)), n.set(d + "", c));
          }
        if (v === void 0)
          (!p || yt(f, a)?.writable) && (v = u(() => /* @__PURE__ */ ge(void 0)), xe(v, Je(o)), n.set(a, v));
        else {
          p = v.v !== M;
          var w = u(() => Je(o));
          xe(v, w);
        }
        var b = Reflect.getOwnPropertyDescriptor(f, a);
        if (b?.set && b.set.call(h, o), !p) {
          if (r && typeof a == "string") {
            var A = (
              /** @type {Source<number>} */
              n.get("length")
            ), S = Number(a);
            Number.isInteger(S) && S >= A.v && xe(A, S + 1);
          }
          kt(i);
        }
        return !0;
      },
      ownKeys(f) {
        O(i);
        var a = Reflect.ownKeys(f).filter((v) => {
          var p = n.get(v);
          return p === void 0 || p.v !== M;
        });
        for (var [o, h] of n)
          h.v !== M && !(o in f) && a.push(o);
        return a;
      },
      setPrototypeOf() {
        ai();
      }
    }
  );
}
var zn, mr, yr, br;
function xi() {
  if (zn === void 0) {
    zn = window, mr = /Firefox/.test(navigator.userAgent);
    var e = Element.prototype, t = Node.prototype, n = Text.prototype;
    yr = yt(t, "firstChild").get, br = yt(t, "nextSibling").get, Pn(e) && (e[un] = void 0, e[Kr] = null, e[Wr] = void 0, e.__e = void 0), Pn(n) && (n[vt] = void 0);
  }
}
function Ue(e = "") {
  return document.createTextNode(e);
}
// @__NO_SIDE_EFFECTS__
function Cn(e) {
  return (
    /** @type {TemplateNode | null} */
    yr.call(e)
  );
}
// @__NO_SIDE_EFFECTS__
function Zt(e) {
  return (
    /** @type {TemplateNode | null} */
    br.call(e)
  );
}
function Ai(e, t) {
  return /* @__PURE__ */ Cn(e);
}
function Bn(e, t = !1) {
  return /* @__PURE__ */ Cn(e);
}
function tn(e, t = 1, n = !1) {
  let r = e;
  for (; t--; )
    r = /** @type {TemplateNode} */
    /* @__PURE__ */ Zt(r);
  return r;
}
function Ci(e) {
  e.textContent = "";
}
function Er() {
  return !1;
}
function Oi(e, t, n) {
  return (
    /** @type {T extends keyof HTMLElementTagNameMap ? HTMLElementTagNameMap[T] : Element} */
    n ? document.createElement(e, { is: n }) : document.createElement(e)
  );
}
function Ri(e) {
  var t = T;
  if (t === null)
    return y.f |= Oe, e;
  if ((t.f & at) === 0 && (t.f & St) === 0)
    throw e;
  de(e, t);
}
function de(e, t) {
  if (!(t !== null && (t.f & q) !== 0)) {
    for (; t !== null; ) {
      if ((t.f & ln) !== 0 && (t.f & (q | fn)) === 0) {
        if ((t.f & at) === 0)
          throw e;
        try {
          t.b.error(e);
          return;
        } catch (n) {
          e = n;
        }
      }
      t = t.parent;
    }
    throw e;
  }
}
function Mi(e) {
  T === null && (y === null && fi(), li()), Re && si();
}
function Ni(e, t) {
  var n = t.last;
  n === null ? t.last = t.first = e : (n.next = e, e.prev = n, t.last = e);
}
function Me(e, t) {
  var n = T;
  n !== null && (n.f & I) !== 0 && (e |= I);
  var r = {
    ctx: j,
    deps: null,
    nodes: null,
    f: e | R | fe,
    first: null,
    fn: t,
    last: null,
    next: null,
    parent: n,
    b: n && n.b,
    prev: null,
    teardown: null,
    wv: 0,
    ac: null
  };
  E?.register_created_effect(r);
  var i = r;
  if ((e & St) !== 0)
    Et !== null ? Et.push(r) : $e.ensure().schedule(r);
  else if (t !== null) {
    try {
      ot(r);
    } catch (u) {
      throw z(r), u;
    }
    i.deps === null && i.teardown === null && i.nodes === null && i.first === i.last && // either `null`, or a singular child
    (i.f & ct) === 0 && (i = i.first, (e & ie) !== 0 && (e & lt) !== 0 && i !== null && (i.f |= lt));
  }
  if (i !== null && (i.parent = n, n !== null && Ni(i, n), y !== null && (y.f & F) !== 0 && (e & ke) === 0)) {
    var s = (
      /** @type {Derived} */
      y
    );
    (s.effects ?? (s.effects = [])).push(i);
  }
  return r;
}
function On() {
  return y !== null && !le;
}
function Fi(e) {
  const t = Me(Kt, null);
  return C(t, N), t.teardown = e, t;
}
function Di(e) {
  Mi();
  var t = (
    /** @type {Effect} */
    T.f
  ), n = !y && (t & X) !== 0 && j !== null && !j.i;
  if (n) {
    var r = (
      /** @type {ComponentContext} */
      j
    );
    (r.e ?? (r.e = [])).push(e);
  } else
    return kr(e);
}
function kr(e) {
  return Me(St | Gr, e);
}
function Pi(e) {
  $e.ensure();
  const t = Me(ke | ct, e);
  return (n = {}) => new Promise((r) => {
    n.outro ? Ye(t, () => {
      z(t), r(void 0);
    }) : (z(t), r(void 0));
  });
}
function Ii(e) {
  return Me(Qe | ct, e);
}
function Li(e, t = 0) {
  return Me(Kt | t, e);
}
function Vn(e, t = [], n = [], r = []) {
  _i(r, t, n, (i) => {
    Me(Kt, () => {
      e(...i.map(O));
    });
  });
}
function Rn(e, t = 0) {
  var n = Me(ie | t, e);
  return n;
}
function J(e) {
  return Me(X | ct, e);
}
function Sr(e) {
  var t = e.teardown;
  if (t !== null) {
    const n = Re, r = y;
    Un(!0), ee(null);
    try {
      t.call(null);
    } catch (i) {
      de(i, e.parent);
    } finally {
      Un(n), ee(r);
    }
  }
}
function Mn(e, t = !1) {
  var n = e.first;
  for (e.first = e.last = null; n !== null; ) {
    const i = n.ac;
    i !== null && Wt(() => {
      i.abort(Mt);
    });
    var r = n.next;
    (n.f & ke) !== 0 ? n.parent = null : z(n, t), n = r;
  }
}
function qi(e) {
  for (var t = e.first; t !== null; ) {
    var n = t.next;
    (t.f & X) === 0 && z(t), t = n;
  }
}
function z(e, t = !0) {
  var n = !1;
  (t || (e.f & $r) !== 0) && e.nodes !== null && e.nodes.end !== null && (ji(
    e.nodes.start,
    /** @type {TemplateNode} */
    e.nodes.end
  ), n = !0), e.f |= fn, Mn(e, t && !n), Tt(e, 0);
  var r = e.nodes && e.nodes.t;
  if (r !== null)
    for (const s of r)
      s.stop();
  Sr(e), e.f ^= fn, e.f |= q;
  var i = e.parent;
  i !== null && i.first !== null && Tr(e), e.next = e.prev = e.teardown = e.ctx = e.deps = e.fn = e.nodes = e.ac = e.b = null;
}
function ji(e, t) {
  for (; e !== null; ) {
    var n = e === t ? null : /* @__PURE__ */ Zt(e);
    e.remove(), e = n;
  }
}
function Tr(e) {
  var t = e.parent, n = e.prev, r = e.next;
  n !== null && (n.next = r), r !== null && (r.prev = n), t !== null && (t.first === e && (t.first = r), t.last === e && (t.last = n));
}
function Ye(e, t, n = !0) {
  var r = [];
  e.f |= En, xr(e, r, !0);
  var i = () => {
    n && z(e), t && t();
  }, s = r.length;
  if (s > 0) {
    var u = () => --s || i();
    for (var f of r)
      f.out(u);
  } else
    i();
}
function xr(e, t, n) {
  if ((e.f & I) === 0) {
    e.f ^= I;
    var r = e.nodes && e.nodes.t;
    if (r !== null)
      for (const f of r)
        (f.is_global || n) && t.push(f);
    for (var i = e.first; i !== null; ) {
      var s = i.next;
      if ((i.f & ke) === 0) {
        var u = (i.f & lt) !== 0 || // If this is a branch effect without a block effect parent,
        // it means the parent block effect was pruned. In that case,
        // transparency information was transferred to the branch effect.
        (i.f & X) !== 0 && (e.f & ie) !== 0;
        xr(i, t, u ? n : !1);
      }
      i = s;
    }
  }
}
function Ut(e) {
  e.f &= ~En, Ar(e, !0);
}
function Ar(e, t) {
  if ((e.f & En) === 0 && (e.f & I) !== 0) {
    e.f ^= I, (e.f & N) === 0 && (C(e, R), $e.ensure().schedule(e));
    for (var n = e.first; n !== null; ) {
      var r = n.next, i = (n.f & lt) !== 0 || (n.f & X) !== 0;
      Ar(n, i ? t : !1), n = r;
    }
    var s = e.nodes && e.nodes.t;
    if (s !== null)
      for (const u of s)
        (u.is_global || t) && u.in();
  }
}
function Nn(e, t) {
  if (e.nodes)
    for (var n = e.nodes.start, r = e.nodes.end; n !== null; ) {
      var i = n === r ? null : /* @__PURE__ */ Zt(n);
      t.append(n), n = i;
    }
}
let It = !1, Re = !1;
function Un(e) {
  Re = e;
}
let y = null, le = !1;
function ee(e) {
  y = e;
}
let T = null;
function _e(e) {
  T = e;
}
let ue = null;
function Cr(e) {
  y !== null && ((y.f & zt) !== 0 || (y.f & F) !== 0) && (ue ?? (ue = /* @__PURE__ */ new Set())).add(e);
}
let L = null, G = 0, K = null;
function zi(e) {
  K = e;
}
let Or = 1, Ie = 0, He = Ie;
function Yn(e) {
  He = e;
}
function Rr() {
  return ++Or;
}
function Nt(e) {
  var t = e.f;
  if ((t & R) !== 0)
    return !0;
  if ((t & Q) !== 0) {
    for (var n = (
      /** @type {Value[]} */
      e.deps
    ), r = n.length, i = 0; i < r; i++) {
      var s = n[i];
      if (Nt(
        /** @type {Derived} */
        s
      ) && ar(
        /** @type {Derived} */
        s
      ), s.wv > e.wv)
        return !0;
    }
    (t & fe) !== 0 && // During time traveling we don't want to reset the status so that
    // traversal of the graph in the other batches still happens
    se === null && C(e, N);
  }
  return !1;
}
function Mr(e, t, n = !0) {
  var r = e.reactions;
  if (r !== null && !(ue !== null && ue.has(e)))
    for (var i = 0; i < r.length; i++) {
      var s = r[i];
      (s.f & F) !== 0 ? Mr(
        /** @type {Derived} */
        s,
        t,
        !1
      ) : t === s && (n ? C(s, R) : (s.f & N) !== 0 && C(s, Q), An(
        /** @type {Effect} */
        s
      ));
    }
}
function Nr(e) {
  var t = L, n = G, r = K, i = y, s = ue, u = j, f = le, a = He, o = e.f;
  L = /** @type {null | Value[]} */
  null, G = 0, K = null, y = (o & (X | ke)) === 0 ? e : null, ue = null, ft(e.ctx), le = !1, He = ++Ie, e.ac !== null && (Wt(() => {
    e.ac.abort(Mt);
  }), e.ac = null);
  try {
    e.f |= zt;
    var h = (
      /** @type {Function} */
      e.fn
    ), v = h();
    e.f |= at;
    var p = Hn(e);
    if (lr() && K !== null && !le && p !== null && (e.f & (F | Q | R)) === 0)
      for (var d = 0; d < /** @type {Source[]} */
      K.length; d++)
        Mr(
          K[d],
          /** @type {Effect} */
          e
        );
    if (i !== null && i !== e) {
      if (Ie++, i.deps !== null)
        for (let c = 0; c < n; c += 1)
          i.deps[c].rv = Ie;
      if (t !== null)
        for (const c of t)
          c.rv = Ie;
      K !== null && (r === null ? r = K : r.push(.../** @type {Source[]} */
      K));
    }
    return (e.f & Oe) !== 0 && (e.f ^= Oe), v;
  } catch (c) {
    return Hn(e), Ri(c);
  } finally {
    e.f ^= zt, L = t, G = n, K = r, y = i, ue = s, ft(u), le = f, He = a;
  }
}
function Hn(e) {
  var i;
  var t = e.deps, n = E?.is_fork;
  if (L !== null) {
    var r;
    if (n || Tt(e, G), t !== null && G > 0)
      for (t.length = G + L.length, r = 0; r < L.length; r++)
        t[G + r] = L[r];
    else
      e.deps = t = L;
    if (On() && (e.f & fe) !== 0)
      for (r = G; r < t.length; r++)
        ((i = t[r]).reactions ?? (i.reactions = [])).push(e);
  } else !n && t !== null && G < t.length && (Tt(e, G), t.length = G);
  return t;
}
function Bi(e, t) {
  let n = t.reactions;
  if (n !== null) {
    var r = zr.call(n, e);
    if (r !== -1) {
      var i = n.length - 1;
      i === 0 ? n = t.reactions = null : (n[r] = n[i], n.pop());
    }
  }
  if (n === null && (t.f & F) !== 0 && // Destroying a child effect while updating a parent effect can cause a dependency to appear
  // to be unused, when in fact it is used by the currently-updating parent. Checking `new_deps`
  // allows us to skip the expensive work of disconnecting and immediately reconnecting it
  (L === null || !qt.call(L, t))) {
    var s = (
      /** @type {Derived} */
      t
    );
    (s.f & fe) !== 0 && (s.f ^= fe), s.v !== M && kn(s), s.ac !== null && Wt(() => {
      s.ac.abort(Mt), s.ac = null, C(s, R);
    }), yi(s), Tt(s, 0);
  }
}
function Tt(e, t) {
  var n = e.deps;
  if (n !== null)
    for (var r = t; r < n.length; r++)
      Bi(e, n[r]);
}
function ot(e) {
  var t = e.f;
  if ((t & q) === 0) {
    C(e, N);
    var n = T, r = It;
    T = e, It = (t & (X | ke)) === 0;
    try {
      (t & (ie | Xn)) !== 0 ? qi(e) : Mn(e), Sr(e);
      var i = Nr(e);
      e.teardown = typeof i == "function" ? i : null, e.wv = Or;
      var s;
    } finally {
      It = r, T = n;
    }
  }
}
function O(e) {
  var t = e.f, n = (t & F) !== 0;
  if (y !== null && !le) {
    var r = T !== null && (T.f & q) !== 0;
    if (!r && (ue === null || !ue.has(e))) {
      var i = y.deps;
      if ((y.f & zt) !== 0)
        e.rv < Ie && (e.rv = Ie, L === null && i !== null && i[G] === e ? G++ : L === null ? L = [e] : L.push(e));
      else {
        y.deps ?? (y.deps = []), qt.call(y.deps, e) || y.deps.push(e);
        var s = e.reactions;
        s === null ? e.reactions = [y] : qt.call(s, y) || s.push(y);
      }
    }
  }
  if (Re && ve.has(e))
    return ve.get(e);
  if (n) {
    var u = (
      /** @type {Derived} */
      e
    );
    if (Re) {
      var f = u.v;
      return ((u.f & N) === 0 && u.reactions !== null || Dr(u)) && (f = Tn(u)), ve.set(u, f), f;
    }
    var a = (u.f & fe) === 0 && !le && y !== null && (It || (y.f & fe) !== 0), o = (u.f & at) === 0;
    Nt(u) && (a && (u.f |= fe), ar(u)), a && !o && (cr(u), Fr(u));
  }
  if (se?.has(e))
    return se.get(e);
  if ((e.f & Oe) !== 0)
    throw e.v;
  return e.v;
}
function Fr(e) {
  if (e.f |= fe, e.deps !== null)
    for (const t of e.deps)
      (t.reactions ?? (t.reactions = [])).push(e), (t.f & F) !== 0 && (t.f & fe) === 0 && (cr(
        /** @type {Derived} */
        t
      ), Fr(
        /** @type {Derived} */
        t
      ));
}
function Dr(e) {
  if (e.v === M) return !0;
  if (e.deps === null) return !1;
  for (const t of e.deps)
    if (ve.has(t) || (t.f & F) !== 0 && Dr(
      /** @type {Derived} */
      t
    ))
      return !0;
  return !1;
}
function Vi(e) {
  var t = le;
  try {
    return le = !0, e();
  } finally {
    le = t;
  }
}
const Ui = ["touchstart", "touchmove"];
function Yi(e) {
  return Ui.includes(e);
}
const Le = /* @__PURE__ */ Symbol("events"), Pr = /* @__PURE__ */ new Set(), _n = /* @__PURE__ */ new Set();
function Hi(e, t, n) {
  (t[Le] ?? (t[Le] = {}))[e] = n;
}
function $i(e) {
  for (var t = 0; t < e.length; t++)
    Pr.add(e[t]);
  for (var n of _n)
    n(e);
}
let nn = null, rn = !1;
function $n(e) {
  var t = this, n = (
    /** @type {Node} */
    t.ownerDocument
  ), r = e.type, i = e.composedPath?.() || [], s = (
    /** @type {null | Element} */
    i[0] || e.target
  );
  nn = e, rn || (rn = !0, setTimeout(() => {
    rn = !1, nn = null;
  }));
  var u = 0, f = nn === e && e[Le];
  if (f) {
    var a = i.indexOf(f);
    if (a !== -1 && (t === document || t === /** @type {any} */
    window)) {
      e[Le] = t;
      return;
    }
    var o = i.indexOf(t);
    if (o === -1)
      return;
    a <= o && (u = a);
  }
  if (s = /** @type {Element} */
  i[u] || e.target, s !== t) {
    Jn(e, "currentTarget", {
      configurable: !0,
      get() {
        return s || n;
      }
    });
    var h = y, v = T;
    ee(null), _e(null);
    try {
      for (var p, d = []; s !== null && s !== t; ) {
        try {
          var c = s[Le]?.[r];
          c != null && (!/** @type {any} */
          s.disabled || // DOM could've been updated already by the time this is reached, so we check this as well
          // -> the target could not have been disabled because it emits the event in the first place
          e.target === s) && c.call(s, e);
        } catch (w) {
          p ? d.push(w) : p = w;
        }
        if (e.cancelBubble) break;
        u++, s = u < i.length ? (
          /** @type {Element} */
          i[u]
        ) : null;
      }
      if (p) {
        for (let w of d)
          queueMicrotask(() => {
            throw w;
          });
        throw p;
      }
    } finally {
      e[Le] = t, delete e.currentTarget, ee(h), _e(v);
    }
  }
}
const Gi = (
  // We gotta write it like this because after downleveling the pure comment may end up in the wrong location
  globalThis?.window?.trustedTypes && /* @__PURE__ */ globalThis.window.trustedTypes.createPolicy("svelte-trusted-html", {
    /** @param {string} html */
    createHTML: (e) => e
  })
);
function Ki(e) {
  return (
    /** @type {string} */
    Gi?.createHTML(e) ?? e
  );
}
function Wi(e) {
  var t = Oi("template");
  return t.innerHTML = Ki(e.replaceAll("<!>", "<!---->")), t.content;
}
function Zi(e, t) {
  var n = (
    /** @type {Effect} */
    T
  );
  n.nodes === null && (n.nodes = { start: e, end: t, a: null, t: null });
}
// @__NO_SIDE_EFFECTS__
function Fn(e, t) {
  var n = (t & Xr) !== 0, r, i = !e.startsWith("<!>");
  return () => {
    r === void 0 && (r = Wi(i ? e : "<!>" + e), r = /** @type {TemplateNode} */
    /* @__PURE__ */ Cn(r));
    var s = (
      /** @type {TemplateNode} */
      n || mr ? document.importNode(r, !0) : r.cloneNode(!0)
    );
    return Zi(s, s), s;
  };
}
function sn(e, t) {
  e !== null && e.before(
    /** @type {Node} */
    t
  );
}
function Ji(e) {
  let t = 0, n = Ge(0), r;
  return () => {
    On() && (O(n), Li(() => (t === 0 && (r = Vi(() => e(() => kt(n)))), t += 1, () => {
      Pe(() => {
        t -= 1, t === 0 && (r?.(), r = void 0, kt(n));
      });
    })));
  };
}
var Qi = lt | ct;
function Xi(e, t, n, r) {
  new es(e, t, n, r);
}
var W, bn, Z, ze, D, Y, P, H, ce, Be, Ce, st, At, Ct, be, Ht, x, ts, ns, pn, rs, gn, wt, Lt, wn, mn;
class es {
  /**
   * @param {TemplateNode} node
   * @param {BoundaryProps} props
   * @param {((anchor: Node) => void)} children
   * @param {((error: unknown) => unknown) | undefined} [transform_error]
   */
  constructor(t, n, r, i) {
    g(this, x);
    /** @type {Boundary | null} */
    V(this, "parent");
    V(this, "is_pending", !1);
    /**
     * API-level transformError transform function. Transforms errors before they reach the `failed` snippet.
     * Inherited from parent boundary, or defaults to identity.
     * @type {(error: unknown) => unknown}
     */
    V(this, "transform_error");
    /** @type {TemplateNode} */
    g(this, W);
    /** @type {TemplateNode | null} */
    g(this, bn, null);
    /** @type {BoundaryProps} */
    g(this, Z);
    /** @type {((anchor: Node) => void)} */
    g(this, ze);
    /** @type {Effect} */
    g(this, D);
    /** @type {Effect | null} */
    g(this, Y, null);
    /** @type {Effect | null} */
    g(this, P, null);
    /** @type {Effect | null} */
    g(this, H, null);
    /** @type {DocumentFragment | null} */
    g(this, ce, null);
    g(this, Be, 0);
    g(this, Ce, 0);
    g(this, st, !1);
    /** @type {Set<Effect>} */
    g(this, At, /* @__PURE__ */ new Set());
    /** @type {Set<Effect>} */
    g(this, Ct, /* @__PURE__ */ new Set());
    /**
     * A source containing the number of pending async deriveds/expressions.
     * Only created if `$effect.pending()` is used inside the boundary,
     * otherwise updating the source results in needless `Batch.ensure()`
     * calls followed by no-op flushes
     * @type {Source<number> | null}
     */
    g(this, be, null);
    g(this, Ht, Ji(() => (_(this, be, Ge(l(this, Be))), () => {
      _(this, be, null);
    })));
    _(this, W, t), _(this, Z, n), _(this, ze, (s) => {
      var u = (
        /** @type {Effect} */
        T
      );
      u.b = this, u.f |= ln, r(s);
    }), this.parent = /** @type {Effect} */
    T.b, this.transform_error = i ?? this.parent?.transform_error ?? ((s) => s), _(this, D, Rn(() => {
      m(this, x, gn).call(this);
    }, Qi));
  }
  /**
   * Defer an effect inside a pending boundary until the boundary resolves
   * @param {Effect} effect
   */
  defer_effect(t) {
    ur(t, l(this, At), l(this, Ct));
  }
  /**
   * Returns `false` if the effect exists inside a boundary whose pending snippet is shown
   * @returns {boolean}
   */
  is_rendered() {
    return !this.is_pending && (!this.parent || this.parent.is_rendered());
  }
  has_pending_snippet() {
    return !!l(this, Z).pending;
  }
  /**
   * Update the source that powers `$effect.pending()` inside this boundary,
   * and controls when the current `pending` snippet (if any) is removed.
   * Do not call from inside the class
   * @param {1 | -1} d
   * @param {Batch} batch
   */
  update_pending_count(t, n) {
    m(this, x, wn).call(this, t, n), _(this, Be, l(this, Be) + t), !(!l(this, be) || l(this, st)) && (_(this, st, !0), Pe(() => {
      _(this, st, !1), l(this, be) && ut(l(this, be), l(this, Be));
    }));
  }
  get_effect_pending() {
    return l(this, Ht).call(this), O(
      /** @type {Source<number>} */
      l(this, be)
    );
  }
  /** @param {unknown} error */
  error(t) {
    if (!l(this, Z).onerror && !l(this, Z).failed)
      throw t;
    E?.is_fork ? (l(this, Y) && E.skip_effect(l(this, Y)), l(this, P) && E.skip_effect(l(this, P)), l(this, H) && E.skip_effect(l(this, H)), E.oncommit(() => {
      m(this, x, mn).call(this, t);
    })) : m(this, x, mn).call(this, t);
  }
}
W = new WeakMap(), bn = new WeakMap(), Z = new WeakMap(), ze = new WeakMap(), D = new WeakMap(), Y = new WeakMap(), P = new WeakMap(), H = new WeakMap(), ce = new WeakMap(), Be = new WeakMap(), Ce = new WeakMap(), st = new WeakMap(), At = new WeakMap(), Ct = new WeakMap(), be = new WeakMap(), Ht = new WeakMap(), x = new WeakSet(), ts = function() {
  try {
    _(this, Y, J(() => l(this, ze).call(this, l(this, W))));
  } catch (t) {
    this.error(t);
  }
}, /**
 * @param {unknown} error The deserialized error from the server's hydration comment
 */
ns = function(t) {
  const n = l(this, Z).failed, { reset: r, invoke_onerror: i } = m(this, x, pn).call(this, t);
  Pe(i), n && _(this, H, J(() => {
    n(
      l(this, W),
      () => t,
      () => r
    );
  }));
}, /**
 * Creates the `reset` function for a failed boundary, along with a function
 * that invokes `onerror` with it (if provided)
 * @param {unknown} error
 * @returns {{ reset: () => void, invoke_onerror: () => void }}
 */
pn = function(t) {
  var n = !1, r = !1;
  const i = () => {
    if (n) {
      ti();
      return;
    }
    n = !0, r && hi(), l(this, H) !== null && Ye(l(this, H), () => {
      _(this, H, null);
    }), m(this, x, Lt).call(this, () => {
      m(this, x, gn).call(this);
    });
  };
  return { reset: i, invoke_onerror: () => {
    try {
      r = !0, l(this, Z).onerror?.(t, i), r = !1;
    } catch (u) {
      de(u, l(this, D) && l(this, D).parent);
    }
  } };
}, rs = function() {
  const t = l(this, Z).pending;
  t && (this.is_pending = !0, _(this, P, J(() => t(l(this, W)))), Pe(() => {
    var n = _(this, ce, document.createDocumentFragment()), r = Ue(), i = !1;
    if (n.append(r), _(this, Y, m(this, x, Lt).call(this, () => {
      try {
        return J(() => l(this, ze).call(this, r));
      } catch (s) {
        try {
          this.error(s), i = !0;
        } catch (u) {
          de(u, l(this, D).parent);
        }
        return null;
      }
    })), l(this, Y) === null) {
      _(this, ce, null), i && m(this, x, wt).call(
        this,
        /** @type {Batch} */
        E
      );
      return;
    }
    l(this, Ce) === 0 && (l(this, W).before(n), _(this, ce, null), Ye(
      /** @type {Effect} */
      l(this, P),
      () => {
        _(this, P, null);
      }
    ), m(this, x, wt).call(
      this,
      /** @type {Batch} */
      E
    ));
  }));
}, gn = function() {
  try {
    if (this.is_pending = this.has_pending_snippet(), _(this, Ce, 0), _(this, Be, 0), _(this, Y, J(() => {
      l(this, ze).call(this, l(this, W));
    })), l(this, Ce) > 0) {
      var t = _(this, ce, document.createDocumentFragment());
      Nn(l(this, Y), t);
      const n = (
        /** @type {(anchor: Node) => void} */
        l(this, Z).pending
      );
      _(this, P, J(() => n(l(this, W))));
    } else
      m(this, x, wt).call(
        this,
        /** @type {Batch} */
        E
      );
  } catch (n) {
    this.error(n);
  }
}, /**
 * @param {Batch} batch
 */
wt = function(t) {
  this.is_pending = !1, t.transfer_effects(l(this, At), l(this, Ct));
}, /**
 * @template T
 * @param {() => T} fn
 */
Lt = function(t) {
  var n = T, r = y, i = j;
  _e(l(this, D)), ee(l(this, D)), ft(l(this, D).ctx);
  try {
    return $e.ensure(), t();
  } finally {
    _e(n), ee(r), ft(i);
  }
}, /**
 * Updates the pending count associated with the currently visible pending snippet,
 * if any, such that we can replace the snippet with content once work is done
 * @param {1 | -1} d
 * @param {Batch} batch
 */
wn = function(t, n) {
  var r;
  if (!this.has_pending_snippet()) {
    this.parent && m(r = this.parent, x, wn).call(r, t, n);
    return;
  }
  _(this, Ce, l(this, Ce) + t), l(this, Ce) === 0 && (m(this, x, wt).call(this, n), l(this, P) && Ye(l(this, P), () => {
    _(this, P, null);
  }), l(this, ce) && (l(this, W).before(l(this, ce)), _(this, ce, null)));
}, /**
 * @param {unknown} error
 */
mn = function(t) {
  l(this, Y) && (z(l(this, Y)), _(this, Y, null)), l(this, P) && (z(l(this, P)), _(this, P, null)), l(this, H) && (z(l(this, H)), _(this, H, null));
  let n = l(this, Z).failed;
  const r = (i) => {
    const { reset: s, invoke_onerror: u } = m(this, x, pn).call(this, i);
    u(), n && _(this, H, m(this, x, Lt).call(this, () => {
      try {
        return J(() => {
          var f = (
            /** @type {Effect} */
            T
          );
          f.b = this, f.f |= ln, n(
            l(this, W),
            () => i,
            () => s
          );
        });
      } catch (f) {
        return de(
          f,
          /** @type {Effect} */
          l(this, D).parent
        ), null;
      }
    }));
  };
  Pe(() => {
    var i;
    try {
      i = this.transform_error(t);
    } catch (s) {
      de(s, l(this, D) && l(this, D).parent);
      return;
    }
    i !== null && typeof i == "object" && typeof /** @type {any} */
    i.then == "function" ? i.then(
      r,
      /** @param {unknown} e */
      (s) => de(s, l(this, D) && l(this, D).parent)
    ) : r(i);
  });
};
function Gn(e, t) {
  var n = t == null ? "" : typeof t == "object" ? `${t}` : t;
  n !== /** @type {any} */
  (e[vt] ?? (e[vt] = e.nodeValue)) && (e[vt] = n, e.nodeValue = `${n}`);
}
function is(e, t) {
  return ss(e, t);
}
const Ft = /* @__PURE__ */ new Map();
function ss(e, { target: t, anchor: n, props: r = {}, events: i, context: s, intro: u = !0, transformError: f }) {
  xi();
  var a = void 0, o = Pi(() => {
    var h = n ?? t.appendChild(Ue());
    Xi(
      /** @type {TemplateNode} */
      h,
      {
        pending: () => {
        }
      },
      (d) => {
        rr({});
        var c = (
          /** @type {ComponentContext} */
          j
        );
        s && (c.c = s), i && (r.$$events = i), a = e(d, r) || sr(), ir();
      },
      f
    );
    var v = /* @__PURE__ */ new Set(), p = (d) => {
      for (var c = 0; c < d.length; c++) {
        var w = d[c];
        if (!v.has(w)) {
          v.add(w);
          var b = Yi(w);
          for (const B of [t, document]) {
            var A = Ft.get(B);
            A === void 0 && (A = /* @__PURE__ */ new Map(), Ft.set(B, A));
            var S = A.get(w);
            S === void 0 ? (B.addEventListener(w, $n, { passive: b }), A.set(w, 1)) : A.set(w, S + 1);
          }
        }
      }
    };
    return p(Gt(Pr)), _n.add(p), () => {
      for (var d of v)
        for (const b of [t, document]) {
          var c = (
            /** @type {Map<string, number>} */
            Ft.get(b)
          ), w = (
            /** @type {number} */
            c.get(d)
          );
          --w == 0 ? (b.removeEventListener(d, $n), c.delete(d), c.size === 0 && Ft.delete(b)) : c.set(d, w);
        }
      _n.delete(p), h !== n && h.parentNode?.removeChild(h);
    };
  });
  return ls.set(a, o), a;
}
let ls = /* @__PURE__ */ new WeakMap();
var re, he, $, Ve, Ot, Rt, $t;
class fs {
  /**
   * @param {TemplateNode} anchor
   * @param {boolean} transition
   */
  constructor(t, n = !0) {
    /** @type {TemplateNode} */
    V(this, "anchor");
    /** @type {Map<Batch, Key>} */
    g(this, re, /* @__PURE__ */ new Map());
    /**
     * Map of keys to effects that are currently rendered in the DOM.
     * These effects are visible and actively part of the document tree.
     * Example:
     * ```
     * {#if condition}
     * 	foo
     * {:else}
     * 	bar
     * {/if}
     * ```
     * Can result in the entries `true->Effect` and `false->Effect`
     * @type {Map<Key, Effect>}
     */
    g(this, he, /* @__PURE__ */ new Map());
    /**
     * Similar to #onscreen with respect to the keys, but contains branches that are not yet
     * in the DOM, because their insertion is deferred.
     * @type {Map<Key, Branch>}
     */
    g(this, $, /* @__PURE__ */ new Map());
    /**
     * Keys of effects that are currently outroing
     * @type {Set<Key>}
     */
    g(this, Ve, /* @__PURE__ */ new Set());
    /**
     * Whether to pause (i.e. outro) on change, or destroy immediately.
     * This is necessary for `<svelte:element>`
     */
    g(this, Ot, !0);
    /**
     * @param {Batch} batch
     */
    g(this, Rt, (t) => {
      if (l(this, re).has(t)) {
        var n = (
          /** @type {Key} */
          l(this, re).get(t)
        ), r = l(this, he).get(n);
        if (r)
          Ut(r), l(this, Ve).delete(n);
        else {
          var i = l(this, $).get(n);
          i && (Ut(i.effect), l(this, he).set(n, i.effect), l(this, $).delete(n), i.fragment.lastChild.remove(), this.anchor.before(i.fragment), r = i.effect);
        }
        for (const [s, u] of l(this, re)) {
          if (l(this, re).delete(s), s === t)
            break;
          const f = l(this, $).get(u);
          f && (z(f.effect), l(this, $).delete(u));
        }
        for (const [s, u] of l(this, he)) {
          if (s === n || l(this, Ve).has(s)) continue;
          const f = () => {
            if (Array.from(l(this, re).values()).includes(s)) {
              var o = document.createDocumentFragment();
              Nn(u, o), o.append(Ue()), l(this, $).set(s, { effect: u, fragment: o });
            } else
              z(u);
            l(this, Ve).delete(s), l(this, he).delete(s);
          };
          l(this, Ot) || !r ? (l(this, Ve).add(s), Ye(u, f, !1)) : f();
        }
      }
    });
    /**
     * @param {Batch} batch
     */
    g(this, $t, (t) => {
      l(this, re).delete(t);
      const n = Array.from(l(this, re).values());
      for (const [r, i] of l(this, $))
        n.includes(r) || (z(i.effect), l(this, $).delete(r));
    });
    this.anchor = t, _(this, Ot, n);
  }
  /**
   *
   * @param {any} key
   * @param {null | ((target: TemplateNode) => void)} fn
   */
  ensure(t, n) {
    var r = (
      /** @type {Batch} */
      E
    ), i = Er();
    if (n && !l(this, he).has(t) && !l(this, $).has(t))
      if (i) {
        var s = document.createDocumentFragment(), u = Ue();
        s.append(u), l(this, $).set(t, {
          effect: J(() => n(u)),
          fragment: s
        });
      } else
        l(this, he).set(
          t,
          J(() => n(this.anchor))
        );
    if (l(this, re).set(r, t), i) {
      for (const [f, a] of l(this, he))
        f === t ? r.unskip_effect(a) : r.skip_effect(a);
      for (const [f, a] of l(this, $))
        f === t ? r.unskip_effect(a.effect) : r.skip_effect(a.effect);
      r.oncommit(l(this, Rt)), r.ondiscard(l(this, $t));
    } else
      l(this, Rt).call(this, r);
  }
}
re = new WeakMap(), he = new WeakMap(), $ = new WeakMap(), Ve = new WeakMap(), Ot = new WeakMap(), Rt = new WeakMap(), $t = new WeakMap();
function us(e, t, n = !1) {
  var r = new fs(e), i = n ? lt : 0;
  function s(u, f) {
    r.ensure(u, f);
  }
  Rn(() => {
    var u = !1;
    t((f, a = 0) => {
      u = !0, s(a, f);
    }), u || s(-1, null);
  }, i);
}
function os(e, t, n) {
  for (var r = [], i = t.length, s, u = t.length, f = 0; f < i; f++) {
    let v = t[f];
    Ye(
      v,
      () => {
        if (s) {
          if (s.pending.delete(v), s.done.add(v), s.pending.size === 0) {
            var p = (
              /** @type {Set<EachOutroGroup>} */
              e.outrogroups
            );
            yn(e, Gt(s.done)), p.delete(s), p.size === 0 && (e.outrogroups = null);
          }
        } else
          u -= 1;
      },
      !1
    );
  }
  if (u === 0) {
    var a = r.length === 0 && n !== null && e.pending.size === 0;
    if (a) {
      var o = (
        /** @type {Element} */
        n
      ), h = (
        /** @type {Element} */
        o.parentNode
      );
      Ci(h), h.append(o), e.items.clear();
    }
    yn(e, t, !a);
  } else
    s = {
      pending: new Set(t),
      done: /* @__PURE__ */ new Set()
    }, (e.outrogroups ?? (e.outrogroups = /* @__PURE__ */ new Set())).add(s);
}
function yn(e, t, n = !0) {
  var r;
  if (e.pending.size > 0) {
    r = /* @__PURE__ */ new Set();
    for (const u of e.pending.values())
      for (const f of u)
        r.add(
          /** @type {EachItem} */
          e.items.get(f).e
        );
  }
  for (var i = 0; i < t.length; i++) {
    var s = t[i];
    if (r?.has(s)) {
      s.f |= Ee;
      const u = document.createDocumentFragment();
      Nn(s, u);
    } else
      z(t[i], n);
  }
}
var Dt;
function as(e, t, n, r, i, s = null) {
  var u = e, f = /* @__PURE__ */ new Map();
  {
    var a = (
      /** @type {Element} */
      e
    );
    u = a.appendChild(Ue());
  }
  var o = null, h = /* @__PURE__ */ wi(() => {
    var S = n();
    return (
      /** @type {V[]} */
      Zn(S) ? S : S == null ? [] : Gt(S)
    );
  }), v, p = /* @__PURE__ */ new Map(), d = !0;
  function c(S) {
    (A.effect.f & q) === 0 && (A.pending.delete(S), A.fallback = o, cs(A, v, u, t, r), o !== null && (v.length === 0 ? (o.f & Ee) === 0 ? Ut(o) : (o.f ^= Ee, mt(o, null, u)) : Ye(o, () => {
      o = null;
    })));
  }
  function w(S) {
    A.pending.delete(S);
  }
  var b = Rn(() => {
    v = /** @type {V[]} */
    O(h);
    for (var S = v.length, B = /* @__PURE__ */ new Set(), oe = (
      /** @type {Batch} */
      E
    ), Ne = Er(), Se = 0; Se < S; Se += 1) {
      var ht = v[Se], te = r(ht, Se), pe = d ? null : f.get(te);
      pe ? (pe.v && ut(pe.v, ht), pe.i && ut(pe.i, Se), Ne && oe.unskip_effect(pe.e)) : (pe = hs(
        f,
        d ? u : Dt ?? (Dt = Ue()),
        ht,
        te,
        Se,
        i,
        t,
        n
      ), d || (pe.e.f |= Ee), f.set(te, pe)), B.add(te);
    }
    if (S === 0 && s && !o && (d ? o = J(() => s(u)) : (o = J(() => s(Dt ?? (Dt = Ue()))), o.f |= Ee)), S > B.size && ii(), !d)
      if (p.set(oe, B), Ne) {
        for (const [Ir, Lr] of f)
          B.has(Ir) || oe.skip_effect(Lr.e);
        oe.oncommit(c), oe.ondiscard(w);
      } else
        c(oe);
    O(h);
  }), A = { effect: b, items: f, pending: p, outrogroups: null, fallback: o };
  d = !1;
}
function dt(e) {
  for (; e !== null && (e.f & X) === 0; )
    e = e.next;
  return e;
}
function cs(e, t, n, r, i) {
  var s = t.length, u = e.items, f = dt(e.effect.first), a, o = null, h = [], v = [], p, d, c, w;
  for (w = 0; w < s; w += 1) {
    if (p = t[w], d = i(p, w), c = /** @type {EachItem} */
    u.get(d).e, e.outrogroups !== null)
      for (const te of e.outrogroups)
        te.pending.delete(c), te.done.delete(c);
    if ((c.f & I) !== 0 && Ut(c), (c.f & Ee) !== 0)
      if (c.f ^= Ee, c === f)
        mt(c, null, n);
      else {
        var b = o ? o.next : f;
        c === e.effect.last && (e.effect.last = c.prev), c.prev && (c.prev.next = c.next), c.next && (c.next.prev = c.prev), Te(e, o, c), Te(e, c, b), mt(c, b, n), o = c, h = [], v = [], f = dt(o.next);
        continue;
      }
    if (c !== f) {
      if (a !== void 0 && a.has(c)) {
        if (h.length < v.length) {
          var A = v[0], S;
          o = A.prev;
          var B = h[0], oe = h[h.length - 1];
          for (S = 0; S < h.length; S += 1)
            mt(h[S], A, n);
          for (S = 0; S < v.length; S += 1)
            a.delete(v[S]);
          Te(e, B.prev, oe.next), Te(e, o, B), Te(e, oe, A), f = A, o = oe, w -= 1, h = [], v = [];
        } else
          a.delete(c), mt(c, f, n), Te(e, c.prev, c.next), Te(e, c, o === null ? e.effect.first : o.next), Te(e, o, c), o = c;
        continue;
      }
      for (h = [], v = []; f !== null && f !== c; )
        (a ?? (a = /* @__PURE__ */ new Set())).add(f), v.push(f), f = dt(f.next);
      if (f === null)
        continue;
    }
    (c.f & Ee) === 0 && h.push(c), o = c, f = dt(c.next);
  }
  if (e.outrogroups !== null) {
    for (const te of e.outrogroups)
      te.pending.size === 0 && (yn(e, Gt(te.done)), e.outrogroups?.delete(te));
    e.outrogroups.size === 0 && (e.outrogroups = null);
  }
  if (f !== null || a !== void 0) {
    var Ne = [];
    if (a !== void 0)
      for (c of a)
        (c.f & I) === 0 && Ne.push(c);
    for (; f !== null; )
      (f.f & I) === 0 && f !== e.fallback && Ne.push(f), f = dt(f.next);
    var Se = Ne.length;
    if (Se > 0) {
      var ht = s === 0 ? n : null;
      os(e, Ne, ht);
    }
  }
}
function hs(e, t, n, r, i, s, u, f) {
  var a = (u & Zr) !== 0 ? (u & Qr) === 0 ? /* @__PURE__ */ Si(n, !1, !1) : Ge(n) : null, o = (u & Jr) !== 0 ? Ge(i) : null;
  return {
    v: a,
    i: o,
    e: J(() => (s(t, a ?? n, o ?? i, f), () => {
      e.delete(r);
    }))
  };
}
function mt(e, t, n) {
  if (e.nodes)
    for (var r = e.nodes.start, i = e.nodes.end, s = t && (t.f & Ee) === 0 ? (
      /** @type {EffectNodes} */
      t.nodes.start
    ) : n; r !== null; ) {
      var u = (
        /** @type {TemplateNode} */
        /* @__PURE__ */ Zt(r)
      );
      if (s.before(r), r === i)
        return;
      r = u;
    }
}
function Te(e, t, n) {
  t === null ? e.effect.first = n : t.next = n, n === null ? e.effect.last = t : n.prev = t;
}
const Kn = [...` 	
\r\f \v\uFEFF`];
function ds(e, t, n) {
  var r = "" + e;
  if (n) {
    for (var i of Object.keys(n))
      if (n[i])
        r = r ? r + " " + i : i;
      else if (r.length)
        for (var s = i.length, u = 0; (u = r.indexOf(i, u)) >= 0; ) {
          var f = u + s;
          (u === 0 || Kn.includes(r[u - 1])) && (f === r.length || Kn.includes(r[f])) ? r = (u === 0 ? "" : r.substring(0, u)) + r.substring(f + 1) : u = f;
        }
  }
  return r === "" ? null : r;
}
function vs(e, t, n, r, i, s) {
  var u = (
    /** @type {any} */
    e[un]
  );
  if (u !== n || u === void 0) {
    var f = ds(n, r, s);
    f == null ? e.removeAttribute("class") : e.className = f, e[un] = n;
  } else if (s && i !== s)
    for (var a in s) {
      var o = !!s[a];
      (i == null || o !== !!i[a]) && e.classList.toggle(a, o);
    }
  return s;
}
const _s = "5";
var Wn;
typeof window < "u" && ((Wn = window.__svelte ?? (window.__svelte = {})).v ?? (Wn.v = /* @__PURE__ */ new Set())).add(_s);
var ps = /* @__PURE__ */ Fn("<li> </li>"), gs = /* @__PURE__ */ Fn('<p class="ok">Svelte 5 mounted and updated</p>'), ws = /* @__PURE__ */ Fn('<div class="svelte-probe"><p data-testid="state"> </p> <ul></ul> <!> <button type="button">Tap to update</button></div>');
function ms(e, t) {
  rr(t, !0);
  let n = /* @__PURE__ */ ge(0), r = Je([{ id: 1, n: "one" }, { id: 2, n: "two" }]), i = /* @__PURE__ */ In(() => O(n) * 2), s = /* @__PURE__ */ In(() => r[r.length - 1].n);
  Di(() => {
    t.onready?.({
      count: O(n),
      doubled: O(i),
      last: O(s)
    });
  });
  function u() {
    xe(n, O(n) + 1), r.push({ id: r.length + 1, n: "item " + (r.length + 1) });
  }
  var f = { bump: u }, a = ws(), o = Ai(a);
  let h;
  var v = Bn(o), p = tn(o, 2);
  as(p, 21, () => r, (b) => b.id, (b, A) => {
    var S = ps(), B = Bn(S, !0);
    Vn(() => Gn(B, O(A).n)), sn(b, S);
  });
  var d = tn(p, 2);
  {
    var c = (b) => {
      var A = gs();
      sn(b, A);
    };
    us(d, (b) => {
      O(n) > 0 && b(c);
    });
  }
  var w = tn(d, 2);
  return Vn(() => {
    h = vs(o, 1, "", null, h, { ok: O(n) > 0 }), Gn(v, `count ${O(n) ?? ""}, doubled ${O(i) ?? ""}, last ${O(s) ?? ""}`);
  }), Hi("click", w, u), sn(e, a), ir(f);
}
$i(["click"]);
const Ze = { mounted: !1, reactive: !1, error: "", version: "5.57.1" };
try {
  const e = document.getElementById("svelte-root");
  e.textContent = "";
  let t = null;
  const n = is(ms, { target: e, props: { onready: (i) => {
    t = i;
  } } });
  qn(), Ze.mounted = !!e.querySelector(".svelte-probe"), n.bump(), qn();
  const r = e.querySelector('[data-testid="state"]').textContent;
  Ze.reactive = r === "count 1, doubled 2, last item 3" && !!t && t.doubled === 2 && e.querySelectorAll("li").length === 3, Ze.reactive || (Ze.error = "unexpected DOM: " + r);
} catch (e) {
  Ze.error = String(e && e.stack ? e.stack.split(`
`)[0] : e);
}
window.__svelteProbe = Ze;
