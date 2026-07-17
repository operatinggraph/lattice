// Vendored nats.js browser client — DO NOT EDIT BY HAND.
// Regenerate with the recipe in ./VENDOR.md.
// @nats-io/nats-core 3.4.0 + @nats-io/jetstream 3.4.0 (transitive: @nats-io/nkeys 2.0.3, @nats-io/nuid 3.0.0)
// Bundled with esbuild 0.24.2 (--bundle --format=esm --platform=browser --target=es2022).
// Authority: https://github.com/nats-io/nats.js (docs/vendors.md). NATS server pin: 2.14.
var __create = Object.create;
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __getProtoOf = Object.getPrototypeOf;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __require = /* @__PURE__ */ ((x) => typeof require !== "undefined" ? require : typeof Proxy !== "undefined" ? new Proxy(x, {
  get: (a, b) => (typeof require !== "undefined" ? require : a)[b]
}) : x)(function(x) {
  if (typeof require !== "undefined") return require.apply(this, arguments);
  throw Error('Dynamic require of "' + x + '" is not supported');
});
var __commonJS = (cb, mod) => function __require2() {
  return mod || (0, cb[__getOwnPropNames(cb)[0]])((mod = { exports: {} }).exports, mod), mod.exports;
};
var __copyProps = (to, from, except, desc) => {
  if (from && typeof from === "object" || typeof from === "function") {
    for (let key of __getOwnPropNames(from))
      if (!__hasOwnProp.call(to, key) && key !== except)
        __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  }
  return to;
};
var __toESM = (mod, isNodeMode, target) => (target = mod != null ? __create(__getProtoOf(mod)) : {}, __copyProps(
  // If the importer is in node compatibility mode or this is not an ESM
  // file that has been converted to a CommonJS file using a Babel-
  // compatible transform (i.e. "__esModule" has not been set), then set
  // "default" to the CommonJS "module.exports" for node compatibility.
  isNodeMode || !mod || !mod.__esModule ? __defProp(target, "default", { value: mod, enumerable: true }) : target,
  mod
));

// node_modules/@nats-io/nats-core/lib/encoders.js
var require_encoders = __commonJS({
  "node_modules/@nats-io/nats-core/lib/encoders.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.TD = exports.TE = exports.Empty = void 0;
    exports.encode = encode;
    exports.decode = decode;
    exports.Empty = new Uint8Array(0);
    exports.TE = new TextEncoder();
    exports.TD = new TextDecoder();
    function concat(...bufs) {
      let max = 0;
      for (let i = 0; i < bufs.length; i++) {
        max += bufs[i].length;
      }
      const out = new Uint8Array(max);
      let index = 0;
      for (let i = 0; i < bufs.length; i++) {
        out.set(bufs[i], index);
        index += bufs[i].length;
      }
      return out;
    }
    function encode(...a) {
      const bufs = [];
      for (let i = 0; i < a.length; i++) {
        bufs.push(exports.TE.encode(a[i]));
      }
      if (bufs.length === 0) {
        return exports.Empty;
      }
      if (bufs.length === 1) {
        return bufs[0];
      }
      return concat(...bufs);
    }
    function decode(a) {
      if (!a || a.length === 0) {
        return "";
      }
      return exports.TD.decode(a);
    }
  }
});

// node_modules/@nats-io/nats-core/lib/errors.js
var require_errors = __commonJS({
  "node_modules/@nats-io/nats-core/lib/errors.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.errors = exports.PermissionViolationError = exports.NoRespondersError = exports.TimeoutError = exports.RequestError = exports.ProtocolError = exports.ConnectionError = exports.DrainingConnectionError = exports.ClosedConnectionError = exports.AuthorizationError = exports.UserAuthenticationExpiredError = exports.InvalidOperationError = exports.InvalidArgumentError = exports.InvalidSubjectError = void 0;
    var InvalidSubjectError = class extends Error {
      constructor(subject, options) {
        super(`illegal subject: '${subject}'`, options);
        this.name = "InvalidSubjectError";
      }
    };
    exports.InvalidSubjectError = InvalidSubjectError;
    var InvalidArgumentError = class _InvalidArgumentError extends Error {
      constructor(message, options) {
        super(message, options);
        this.name = "InvalidArgumentError";
      }
      static format(property, message, options) {
        if (Array.isArray(message) && message.length > 1) {
          message = message[0];
        }
        if (Array.isArray(property)) {
          property = property.map((n) => `'${n}'`);
          property = property.join(",");
        } else {
          property = `'${property}'`;
        }
        return new _InvalidArgumentError(`${property} ${message}`, options);
      }
    };
    exports.InvalidArgumentError = InvalidArgumentError;
    var InvalidOperationError = class extends Error {
      constructor(message, options) {
        super(message, options);
        this.name = "InvalidOperationError";
      }
    };
    exports.InvalidOperationError = InvalidOperationError;
    var UserAuthenticationExpiredError = class _UserAuthenticationExpiredError extends Error {
      constructor(message, options) {
        super(message, options);
        this.name = "UserAuthenticationExpiredError";
      }
      static parse(s) {
        const ss = s.toLowerCase();
        if (ss.indexOf("user authentication expired") !== -1) {
          return new _UserAuthenticationExpiredError(s);
        }
        return null;
      }
    };
    exports.UserAuthenticationExpiredError = UserAuthenticationExpiredError;
    var AuthorizationError = class _AuthorizationError extends Error {
      constructor(message, options) {
        super(message, options);
        this.name = "AuthorizationError";
      }
      static parse(s) {
        const messages = [
          "authorization violation",
          "account authentication expired",
          "authentication timeout"
        ];
        const ss = s.toLowerCase();
        for (let i = 0; i < messages.length; i++) {
          if (ss.indexOf(messages[i]) !== -1) {
            return new _AuthorizationError(s);
          }
        }
        return null;
      }
    };
    exports.AuthorizationError = AuthorizationError;
    var ClosedConnectionError = class extends Error {
      constructor() {
        super("closed connection");
        this.name = "ClosedConnectionError";
      }
    };
    exports.ClosedConnectionError = ClosedConnectionError;
    var DrainingConnectionError = class extends Error {
      constructor() {
        super("connection draining");
        this.name = "DrainingConnectionError";
      }
    };
    exports.DrainingConnectionError = DrainingConnectionError;
    var ConnectionError = class extends Error {
      constructor(message, options) {
        super(message, options);
        this.name = "ConnectionError";
      }
    };
    exports.ConnectionError = ConnectionError;
    var ProtocolError = class extends Error {
      constructor(message, options) {
        super(message, options);
        this.name = "ProtocolError";
      }
    };
    exports.ProtocolError = ProtocolError;
    var RequestError = class extends Error {
      constructor(message = "", options) {
        super(message, options);
        this.name = "RequestError";
      }
      isNoResponders() {
        return this.cause instanceof NoRespondersError;
      }
    };
    exports.RequestError = RequestError;
    var TimeoutError = class extends Error {
      constructor(options) {
        super("timeout", options);
        this.name = "TimeoutError";
      }
    };
    exports.TimeoutError = TimeoutError;
    var NoRespondersError = class extends Error {
      subject;
      constructor(subject, options) {
        super(`no responders: '${subject}'`, options);
        this.subject = subject;
        this.name = "NoResponders";
      }
    };
    exports.NoRespondersError = NoRespondersError;
    var PermissionViolationError = class _PermissionViolationError extends Error {
      operation;
      subject;
      queue;
      constructor(message, operation, subject, queue, options) {
        super(message, options);
        this.name = "PermissionViolationError";
        this.operation = operation;
        this.subject = subject;
        this.queue = queue;
      }
      static parse(s) {
        const t = s ? s.toLowerCase() : "";
        if (t.indexOf("permissions violation") === -1) {
          return null;
        }
        let operation = "publish";
        let subject = "";
        let queue = void 0;
        const m = s.match(/(Publish|Subscription) to "(\S+)"/);
        if (m) {
          operation = m[1].toLowerCase();
          subject = m[2];
          if (operation === "subscription") {
            const qm = s.match(/using queue "(\S+)"/);
            if (qm) {
              queue = qm[1];
            }
          }
        }
        return new _PermissionViolationError(s, operation, subject, queue);
      }
    };
    exports.PermissionViolationError = PermissionViolationError;
    exports.errors = {
      AuthorizationError,
      ClosedConnectionError,
      ConnectionError,
      DrainingConnectionError,
      InvalidArgumentError,
      InvalidOperationError,
      InvalidSubjectError,
      NoRespondersError,
      PermissionViolationError,
      ProtocolError,
      RequestError,
      TimeoutError,
      UserAuthenticationExpiredError
    };
  }
});

// node_modules/@nats-io/nats-core/lib/util.js
var require_util = __commonJS({
  "node_modules/@nats-io/nats-core/lib/util.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.SimpleMutex = exports.Perf = void 0;
    exports.extend = extend;
    exports.render = render;
    exports.timeout = timeout;
    exports.delay = delay;
    exports.deadline = deadline;
    exports.deferred = deferred;
    exports.debugDeferred = debugDeferred;
    exports.shuffle = shuffle;
    exports.collect = collect;
    exports.jitter = jitter;
    exports.backoff = backoff;
    exports.nanos = nanos;
    exports.millis = millis;
    exports.randomToken = randomToken;
    var encoders_1 = require_encoders();
    var errors_1 = require_errors();
    function extend(a, ...b) {
      for (let i = 0; i < b.length; i++) {
        const o = b[i];
        Object.keys(o).forEach(function(k) {
          a[k] = o[k];
        });
      }
      return a;
    }
    function render(frame) {
      const cr = "\u240D";
      const lf = "\u240A";
      return encoders_1.TD.decode(frame).replace(/\n/g, lf).replace(/\r/g, cr);
    }
    function timeout(ms, asyncTraces = true) {
      const err = asyncTraces ? new errors_1.TimeoutError() : null;
      let methods;
      let timer;
      const p = new Promise((_resolve, reject) => {
        const cancel = () => {
          if (timer) {
            clearTimeout(timer);
          }
        };
        methods = { cancel };
        timer = setTimeout(() => {
          if (err === null) {
            reject(new errors_1.TimeoutError());
          } else {
            reject(err);
          }
        }, ms);
      });
      return Object.assign(p, methods);
    }
    function delay(ms = 0) {
      let methods;
      const p = new Promise((resolve) => {
        const timer = setTimeout(() => {
          resolve();
        }, ms);
        const cancel = () => {
          if (timer) {
            clearTimeout(timer);
            resolve();
          }
        };
        methods = { cancel };
      });
      return Object.assign(p, methods);
    }
    async function deadline(p, millis2 = 1e3) {
      const d = deferred();
      const timer = setTimeout(() => {
        d.reject(new errors_1.TimeoutError());
      }, millis2);
      try {
        return await Promise.race([p, d]);
      } finally {
        clearTimeout(timer);
      }
    }
    function deferred() {
      let methods = {};
      const p = new Promise((resolve, reject) => {
        methods = { resolve, reject };
      });
      return Object.assign(p, methods);
    }
    function debugDeferred() {
      let methods = {};
      const p = new Promise((resolve, reject) => {
        methods = {
          resolve: (v) => {
            console.trace("resolve", v);
            resolve(v);
          },
          reject: (err) => {
            console.trace("reject");
            reject(err);
          }
        };
      });
      return Object.assign(p, methods);
    }
    function shuffle(a) {
      for (let i = a.length - 1; i > 0; i--) {
        const j = Math.floor(Math.random() * (i + 1));
        [a[i], a[j]] = [a[j], a[i]];
      }
      return a;
    }
    async function collect(iter) {
      const buf = [];
      for await (const v of iter) {
        buf.push(v);
      }
      return buf;
    }
    var Perf = class {
      timers;
      measures;
      constructor() {
        this.timers = /* @__PURE__ */ new Map();
        this.measures = /* @__PURE__ */ new Map();
      }
      mark(key) {
        this.timers.set(key, performance.now());
      }
      measure(key, startKey, endKey) {
        const s = this.timers.get(startKey);
        if (s === void 0) {
          throw new Error(`${startKey} is not defined`);
        }
        const e = this.timers.get(endKey);
        if (e === void 0) {
          throw new Error(`${endKey} is not defined`);
        }
        this.measures.set(key, e - s);
      }
      getEntries() {
        const values = [];
        this.measures.forEach((v, k) => {
          values.push({ name: k, duration: v });
        });
        return values;
      }
    };
    exports.Perf = Perf;
    var SimpleMutex = class {
      max;
      current;
      waiting;
      /**
       * @param max number of concurrent operations
       */
      constructor(max = 1) {
        this.max = max;
        this.current = 0;
        this.waiting = [];
      }
      /**
       * Returns a promise that resolves when the mutex is acquired
       */
      lock() {
        this.current++;
        if (this.current <= this.max) {
          return Promise.resolve();
        }
        const d = deferred();
        this.waiting.push(d);
        return d;
      }
      /**
       * Release an acquired mutex - must be called
       */
      unlock() {
        this.current--;
        const d = this.waiting.pop();
        d?.resolve();
      }
    };
    exports.SimpleMutex = SimpleMutex;
    function jitter(n) {
      if (n === 0) {
        return 0;
      }
      return Math.floor(n / 2 + Math.random() * n);
    }
    function backoff(policy = [0, 250, 250, 500, 500, 3e3, 5e3]) {
      if (!Array.isArray(policy)) {
        policy = [0, 250, 250, 500, 500, 3e3, 5e3];
      }
      const max = policy.length - 1;
      return {
        backoff(attempt) {
          return jitter(attempt > max ? policy[max] : policy[attempt]);
        }
      };
    }
    function nanos(millis2) {
      return millis2 * 1e6;
    }
    function millis(ns) {
      return Math.floor(ns / 1e6);
    }
    var tokenDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
    var tokenDigitCodes = new Uint8Array(62);
    for (let i = 0; i < 62; i++)
      tokenDigitCodes[i] = tokenDigits.charCodeAt(i);
    var tokenSpace = 218340105584896;
    function randomToken() {
      let n = Math.floor(Math.random() * tokenSpace);
      let d = n % 62;
      const c0 = tokenDigitCodes[d];
      n = (n - d) / 62;
      d = n % 62;
      const c1 = tokenDigitCodes[d];
      n = (n - d) / 62;
      d = n % 62;
      const c2 = tokenDigitCodes[d];
      n = (n - d) / 62;
      d = n % 62;
      const c3 = tokenDigitCodes[d];
      n = (n - d) / 62;
      d = n % 62;
      const c4 = tokenDigitCodes[d];
      n = (n - d) / 62;
      d = n % 62;
      const c5 = tokenDigitCodes[d];
      n = (n - d) / 62;
      d = n % 62;
      const c6 = tokenDigitCodes[d];
      n = (n - d) / 62;
      const c7 = tokenDigitCodes[n];
      return String.fromCharCode(c0, c1, c2, c3, c4, c5, c6, c7);
    }
  }
});

// node_modules/@nats-io/nuid/lib/nuid.js
var require_nuid = __commonJS({
  "node_modules/@nats-io/nuid/lib/nuid.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.nuid = exports.NuidImpl = void 0;
    var digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
    var base = 62;
    var preLen = 12;
    var seqLen = 10;
    var minInc = 33;
    var maxInc = 333;
    var totalLen = preLen + seqLen;
    var DIGIT_CODES = new Uint8Array(base);
    for (let i = 0; i < base; i++)
      DIGIT_CODES[i] = digits.charCodeAt(i);
    var TWO32 = 4294967296;
    var MAX_SEQ = BigInt(base) ** BigInt(seqLen);
    var MAX_HI = Number(MAX_SEQ / (1n << 32n));
    var MAX_LO = Number(MAX_SEQ % (1n << 32n));
    function _getRandomValues(a) {
      for (let i = 0; i < a.length; i++) {
        a[i] = Math.floor(Math.random() * 256);
      }
    }
    function fillRandom(a) {
      if (globalThis?.crypto?.getRandomValues) {
        globalThis.crypto.getRandomValues(a);
      } else {
        _getRandomValues(a);
      }
    }
    var NuidImpl = class {
      buf;
      cbuf;
      seqHi;
      seqLo;
      inc;
      inited;
      constructor() {
        this.buf = new Uint8Array(totalLen);
        this.cbuf = new Uint8Array(preLen);
        this.inited = false;
      }
      /**
       * Initializes a nuid with a crypto random prefix,
       * and pseudo-random sequence and increment. This function
       * is only called if any api on a nuid is called.
       *
       * @ignore
       */
      init() {
        this.inited = true;
        this.setPre();
        this.initSeqAndInc();
        this.fillSeq();
      }
      /**
       * Initializes the pseudo random sequence number and the increment range.
       * @ignore
       */
      initSeqAndInc() {
        let tries = 0;
        do {
          this.seqHi = Math.floor(Math.random() * (MAX_HI + 1));
          this.seqLo = Math.floor(Math.random() * TWO32);
        } while (tries++ < 8 && this.seqHi === MAX_HI && this.seqLo >= MAX_LO);
        if (this.seqHi === MAX_HI && this.seqLo >= MAX_LO) {
          this.seqLo = 0;
        }
        this.inc = Math.random() * (maxInc - minInc) + minInc | 0;
      }
      /**
       * Sets the prefix from crypto random bytes. Converts them to base62.
       *
       * @ignore
       */
      setPre() {
        fillRandom(this.cbuf);
        for (let i = 0; i < preLen; i++) {
          this.buf[i] = DIGIT_CODES[this.cbuf[i] % base];
        }
      }
      /**
       * Fills the sequence portion of the buffer as base62 from
       * the split-int seq (seqHi, seqLo). Performs long division
       * by 62 over the 64-bit value using doubles only — no bigint.
       *
       * @ignore
       */
      fillSeq() {
        let hi = this.seqHi;
        let lo = this.seqLo;
        for (let i = totalLen - 1; i >= preLen; i--) {
          const hiQ = Math.floor(hi / base);
          const hiR = hi - hiQ * base;
          const combined = hiR * TWO32 + lo;
          const loQ = Math.floor(combined / base);
          const rem = combined - loQ * base;
          this.buf[i] = DIGIT_CODES[rem];
          hi = hiQ;
          lo = loQ;
        }
      }
      /**
       * Returns the next nuid.
       */
      next() {
        if (!this.inited) {
          this.init();
        }
        this.seqLo += this.inc;
        if (this.seqLo >= TWO32) {
          this.seqLo -= TWO32;
          this.seqHi += 1;
        }
        if (this.seqHi > MAX_HI || this.seqHi === MAX_HI && this.seqLo >= MAX_LO) {
          this.setPre();
          this.initSeqAndInc();
        }
        this.fillSeq();
        const b = this.buf;
        return String.fromCharCode(b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15], b[16], b[17], b[18], b[19], b[20], b[21]);
      }
      /**
       * Resets the prefix and counter for the nuid. This is typically
       * called automatically from within next() if the current sequence
       * exceeds the resolution of the nuid.
       */
      reset() {
        this.init();
      }
    };
    exports.NuidImpl = NuidImpl;
    exports.nuid = new NuidImpl();
  }
});

// node_modules/@nats-io/nuid/lib/mod.js
var require_mod = __commonJS({
  "node_modules/@nats-io/nuid/lib/mod.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.nuid = exports.Nuid = void 0;
    var nuid_ts_1 = require_nuid();
    exports.Nuid = nuid_ts_1.NuidImpl;
    exports.nuid = nuid_ts_1.nuid;
  }
});

// node_modules/@nats-io/nats-core/lib/nuid.js
var require_nuid2 = __commonJS({
  "node_modules/@nats-io/nats-core/lib/nuid.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.nuid = exports.Nuid = void 0;
    var nuid_1 = require_mod();
    Object.defineProperty(exports, "Nuid", { enumerable: true, get: function() {
      return nuid_1.Nuid;
    } });
    Object.defineProperty(exports, "nuid", { enumerable: true, get: function() {
      return nuid_1.nuid;
    } });
  }
});

// node_modules/@nats-io/nats-core/lib/core.js
var require_core = __commonJS({
  "node_modules/@nats-io/nats-core/lib/core.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.DEFAULT_HOST = exports.DEFAULT_PORT = exports.Match = void 0;
    exports.syncIterator = syncIterator;
    exports.createInbox = createInbox;
    var nuid_1 = require_nuid2();
    var errors_1 = require_errors();
    exports.Match = {
      // Exact option is case-sensitive
      Exact: "exact",
      // Case-sensitive, but key is transformed to Canonical MIME representation
      CanonicalMIME: "canonical",
      // Case-insensitive matches
      IgnoreCase: "insensitive"
    };
    function syncIterator(src) {
      const iter = src[Symbol.asyncIterator]();
      return {
        async next() {
          const m = await iter.next();
          if (m.done) {
            return Promise.resolve(null);
          }
          return Promise.resolve(m.value);
        }
      };
    }
    function createInbox(prefix = "") {
      prefix = prefix || "_INBOX";
      if (typeof prefix !== "string") {
        throw new TypeError("prefix must be a string");
      }
      prefix.split(".").forEach((v) => {
        if (v === "*" || v === ">") {
          throw errors_1.InvalidArgumentError.format("prefix", `cannot have wildcards ('${prefix}')`);
        }
      });
      return `${prefix}.${nuid_1.nuid.next()}`;
    }
    exports.DEFAULT_PORT = 4222;
    exports.DEFAULT_HOST = "127.0.0.1";
  }
});

// node_modules/@nats-io/nats-core/lib/databuffer.js
var require_databuffer = __commonJS({
  "node_modules/@nats-io/nats-core/lib/databuffer.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.DataBuffer = void 0;
    var encoders_1 = require_encoders();
    var DataBuffer = class {
      buffers;
      byteLength;
      constructor() {
        this.buffers = [];
        this.byteLength = 0;
      }
      static concat(...bufs) {
        let max = 0;
        for (let i = 0; i < bufs.length; i++) {
          max += bufs[i].length;
        }
        const out = new Uint8Array(max);
        let index = 0;
        for (let i = 0; i < bufs.length; i++) {
          out.set(bufs[i], index);
          index += bufs[i].length;
        }
        return out;
      }
      static fromAscii(m) {
        if (!m) {
          m = "";
        }
        return encoders_1.TE.encode(m);
      }
      static toAscii(a) {
        return encoders_1.TD.decode(a);
      }
      reset() {
        this.buffers.length = 0;
        this.byteLength = 0;
      }
      pack() {
        if (this.buffers.length > 1) {
          const v = new Uint8Array(this.byteLength);
          let index = 0;
          for (let i = 0; i < this.buffers.length; i++) {
            v.set(this.buffers[i], index);
            index += this.buffers[i].length;
          }
          this.buffers.length = 0;
          this.buffers.push(v);
        }
      }
      shift() {
        if (this.buffers.length) {
          const a = this.buffers.shift();
          if (a) {
            this.byteLength -= a.length;
            return a;
          }
        }
        return new Uint8Array(0);
      }
      drain(n) {
        if (this.buffers.length) {
          this.pack();
          const v = this.buffers.pop();
          if (v) {
            const max = this.byteLength;
            if (n === void 0 || n > max) {
              n = max;
            }
            const d = v.subarray(0, n);
            if (max > n) {
              this.buffers.push(v.subarray(n));
            }
            this.byteLength = max - n;
            return d;
          }
        }
        return new Uint8Array(0);
      }
      fill(a, ...bufs) {
        if (a) {
          this.buffers.push(a);
          this.byteLength += a.length;
        }
        for (let i = 0; i < bufs.length; i++) {
          if (bufs[i] && bufs[i].length) {
            this.buffers.push(bufs[i]);
            this.byteLength += bufs[i].length;
          }
        }
      }
      peek() {
        if (this.buffers.length) {
          this.pack();
          return this.buffers[0];
        }
        return new Uint8Array(0);
      }
      size() {
        return this.byteLength;
      }
      length() {
        return this.buffers.length;
      }
    };
    exports.DataBuffer = DataBuffer;
  }
});

// node_modules/@nats-io/nats-core/lib/transport.js
var require_transport = __commonJS({
  "node_modules/@nats-io/nats-core/lib/transport.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.LF = exports.CR = exports.CRLF = exports.CR_LF_LEN = exports.CR_LF = void 0;
    exports.setTransportFactory = setTransportFactory;
    exports.defaultPort = defaultPort;
    exports.getUrlParseFn = getUrlParseFn;
    exports.newTransport = newTransport;
    exports.getResolveFn = getResolveFn;
    exports.protoLen = protoLen;
    exports.extractProtocolMessage = extractProtocolMessage;
    var encoders_1 = require_encoders();
    var core_1 = require_core();
    var databuffer_1 = require_databuffer();
    var transportConfig;
    function setTransportFactory(config) {
      transportConfig = config;
    }
    function defaultPort() {
      return transportConfig !== void 0 && transportConfig.defaultPort !== void 0 ? transportConfig.defaultPort : core_1.DEFAULT_PORT;
    }
    function getUrlParseFn() {
      return transportConfig !== void 0 && transportConfig.urlParseFn ? transportConfig.urlParseFn : void 0;
    }
    function newTransport() {
      if (!transportConfig || typeof transportConfig.factory !== "function") {
        throw new Error("transport fn is not set");
      }
      return transportConfig.factory();
    }
    function getResolveFn() {
      return transportConfig !== void 0 && transportConfig.dnsResolveFn ? transportConfig.dnsResolveFn : void 0;
    }
    exports.CR_LF = "\r\n";
    exports.CR_LF_LEN = exports.CR_LF.length;
    exports.CRLF = databuffer_1.DataBuffer.fromAscii(exports.CR_LF);
    exports.CR = new Uint8Array(exports.CRLF)[0];
    exports.LF = new Uint8Array(exports.CRLF)[1];
    function protoLen(ba) {
      for (let i = 0; i < ba.length; i++) {
        const n = i + 1;
        if (ba.byteLength > n && ba[i] === exports.CR && ba[n] === exports.LF) {
          return n + 1;
        }
      }
      return 0;
    }
    function extractProtocolMessage(a) {
      const len = protoLen(a);
      if (len > 0) {
        const ba = new Uint8Array(a);
        const out = ba.slice(0, len);
        return encoders_1.TD.decode(out);
      }
      return "";
    }
  }
});

// node_modules/@nats-io/nats-core/lib/ipparser.js
var require_ipparser = __commonJS({
  "node_modules/@nats-io/nats-core/lib/ipparser.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.ipV4 = ipV4;
    exports.isIP = isIP;
    exports.parseIP = parseIP;
    var IPv4LEN = 4;
    var IPv6LEN = 16;
    var ASCII0 = 48;
    var ASCII9 = 57;
    var ASCIIA = 65;
    var ASCIIF = 70;
    var ASCIIa = 97;
    var ASCIIf = 102;
    var big = 16777215;
    function ipV4(a, b, c, d) {
      const ip = new Uint8Array(IPv6LEN);
      const prefix = [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255];
      prefix.forEach((v, idx) => {
        ip[idx] = v;
      });
      ip[12] = a;
      ip[13] = b;
      ip[14] = c;
      ip[15] = d;
      return ip;
    }
    function isIP(h) {
      return parseIP(h) !== void 0;
    }
    function parseIP(h) {
      for (let i = 0; i < h.length; i++) {
        switch (h[i]) {
          case ".":
            return parseIPv4(h);
          case ":":
            return parseIPv6(h);
        }
      }
      return;
    }
    function parseIPv4(s) {
      const ip = new Uint8Array(IPv4LEN);
      for (let i = 0; i < IPv4LEN; i++) {
        if (s.length === 0) {
          return void 0;
        }
        if (i > 0) {
          if (s[0] !== ".") {
            return void 0;
          }
          s = s.substring(1);
        }
        const { n, c, ok } = dtoi(s);
        if (!ok || n > 255) {
          return void 0;
        }
        s = s.substring(c);
        ip[i] = n;
      }
      return ipV4(ip[0], ip[1], ip[2], ip[3]);
    }
    function parseIPv6(s) {
      const ip = new Uint8Array(IPv6LEN);
      let ellipsis = -1;
      if (s.length >= 2 && s[0] === ":" && s[1] === ":") {
        ellipsis = 0;
        s = s.substring(2);
        if (s.length === 0) {
          return ip;
        }
      }
      let i = 0;
      while (i < IPv6LEN) {
        const { n, c, ok } = xtoi(s);
        if (!ok || n > 65535) {
          return void 0;
        }
        if (c < s.length && s[c] === ".") {
          if (ellipsis < 0 && i != IPv6LEN - IPv4LEN) {
            return void 0;
          }
          if (i + IPv4LEN > IPv6LEN) {
            return void 0;
          }
          const ip4 = parseIPv4(s);
          if (ip4 === void 0) {
            return void 0;
          }
          ip[i] = ip4[12];
          ip[i + 1] = ip4[13];
          ip[i + 2] = ip4[14];
          ip[i + 3] = ip4[15];
          s = "";
          i += IPv4LEN;
          break;
        }
        ip[i] = n >> 8;
        ip[i + 1] = n;
        i += 2;
        s = s.substring(c);
        if (s.length === 0) {
          break;
        }
        if (s[0] !== ":" || s.length == 1) {
          return void 0;
        }
        s = s.substring(1);
        if (s[0] === ":") {
          if (ellipsis >= 0) {
            return void 0;
          }
          ellipsis = i;
          s = s.substring(1);
          if (s.length === 0) {
            break;
          }
        }
      }
      if (s.length !== 0) {
        return void 0;
      }
      if (i < IPv6LEN) {
        if (ellipsis < 0) {
          return void 0;
        }
        const n = IPv6LEN - i;
        for (let j = i - 1; j >= ellipsis; j--) {
          ip[j + n] = ip[j];
        }
        for (let j = ellipsis + n - 1; j >= ellipsis; j--) {
          ip[j] = 0;
        }
      } else if (ellipsis >= 0) {
        return void 0;
      }
      return ip;
    }
    function dtoi(s) {
      let i = 0;
      let n = 0;
      for (i = 0; i < s.length && ASCII0 <= s.charCodeAt(i) && s.charCodeAt(i) <= ASCII9; i++) {
        n = n * 10 + (s.charCodeAt(i) - ASCII0);
        if (n >= big) {
          return { n: big, c: i, ok: false };
        }
      }
      if (i === 0) {
        return { n: 0, c: 0, ok: false };
      }
      return { n, c: i, ok: true };
    }
    function xtoi(s) {
      let n = 0;
      let i = 0;
      for (i = 0; i < s.length; i++) {
        if (ASCII0 <= s.charCodeAt(i) && s.charCodeAt(i) <= ASCII9) {
          n *= 16;
          n += s.charCodeAt(i) - ASCII0;
        } else if (ASCIIa <= s.charCodeAt(i) && s.charCodeAt(i) <= ASCIIf) {
          n *= 16;
          n += s.charCodeAt(i) - ASCIIa + 10;
        } else if (ASCIIA <= s.charCodeAt(i) && s.charCodeAt(i) <= ASCIIF) {
          n *= 16;
          n += s.charCodeAt(i) - ASCIIA + 10;
        } else {
          break;
        }
        if (n >= big) {
          return { n: 0, c: i, ok: false };
        }
      }
      if (i === 0) {
        return { n: 0, c: i, ok: false };
      }
      return { n, c: i, ok: true };
    }
  }
});

// node_modules/@nats-io/nats-core/lib/servers.js
var require_servers = __commonJS({
  "node_modules/@nats-io/nats-core/lib/servers.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.Servers = exports.ServerImpl = void 0;
    exports.isIPV4OrHostname = isIPV4OrHostname;
    exports.hostPort = hostPort;
    var transport_1 = require_transport();
    var util_1 = require_util();
    var ipparser_1 = require_ipparser();
    var core_1 = require_core();
    var errors_1 = require_errors();
    function isIPV4OrHostname(hp) {
      if (hp.indexOf("[") !== -1 || hp.indexOf("::") !== -1) {
        return false;
      }
      if (hp.indexOf(".") !== -1) {
        return true;
      }
      if (hp.split(":").length <= 2) {
        return true;
      }
      return false;
    }
    function isIPV6(hp) {
      return !isIPV4OrHostname(hp);
    }
    function filterIpv6MappedToIpv4(hp) {
      const prefix = "::FFFF:";
      const idx = hp.toUpperCase().indexOf(prefix);
      if (idx !== -1 && hp.indexOf(".") !== -1) {
        let ip = hp.substring(idx + prefix.length);
        ip = ip.replace("[", "");
        return ip.replace("]", "");
      }
      return hp;
    }
    function hostPort(u) {
      u = u.trim();
      if (u.match(/^(.*:\/\/)(.*)/m)) {
        u = u.replace(/^(.*:\/\/)(.*)/gm, "$2");
      }
      u = filterIpv6MappedToIpv4(u);
      if (isIPV6(u) && u.indexOf("[") === -1) {
        u = `[${u}]`;
      }
      const op = isIPV6(u) ? u.match(/(]:)(\d+)/) : u.match(/(:)(\d+)/);
      const port = op && op.length === 3 && op[1] && op[2] ? parseInt(op[2]) : core_1.DEFAULT_PORT;
      const protocol = port === 80 ? "https" : "http";
      const url = new URL(`${protocol}://${u}`);
      url.port = `${port}`;
      let hostname = url.hostname;
      if (hostname.charAt(0) === "[") {
        hostname = hostname.substring(1, hostname.length - 1);
      }
      const listen = url.host;
      return { listen, hostname, port };
    }
    var ServerImpl = class _ServerImpl {
      src;
      listen;
      hostname;
      port;
      didConnect;
      reconnects;
      lastConnect;
      gossiped;
      tlsName;
      resolves;
      constructor(u, gossiped = false) {
        this.src = u;
        this.tlsName = "";
        const v = hostPort(u);
        this.listen = v.listen;
        this.hostname = v.hostname;
        this.port = v.port;
        this.didConnect = false;
        this.reconnects = 0;
        this.lastConnect = 0;
        this.gossiped = gossiped;
      }
      toString() {
        return this.listen;
      }
      async resolve(opts) {
        if (!opts.fn || opts.resolve === false) {
          return [this];
        }
        const buf = [];
        if ((0, ipparser_1.isIP)(this.hostname)) {
          return [this];
        } else {
          const ips = await opts.fn(this.hostname);
          if (opts.debug) {
            console.log(`resolve ${this.hostname} = ${ips.join(",")}`);
          }
          for (const ip of ips) {
            const proto = this.port === 80 ? "https" : "http";
            const url = new URL(`${proto}://${isIPV6(ip) ? "[" + ip + "]" : ip}`);
            url.port = `${this.port}`;
            const ss = new _ServerImpl(url.host, false);
            ss.tlsName = this.hostname;
            buf.push(ss);
          }
        }
        if (opts.randomize) {
          (0, util_1.shuffle)(buf);
        }
        this.resolves = buf;
        return buf;
      }
    };
    exports.ServerImpl = ServerImpl;
    var Servers = class _Servers {
      firstSelect;
      servers;
      currentServer;
      tlsName;
      randomize;
      constructor(opts = {}) {
        this.firstSelect = true;
        this.servers = [];
        this.tlsName = "";
        this.randomize = opts.randomize || false;
      }
      /**
       * Replace the server pool with the provided list of `host:port` entries.
       *
       * Throws `InvalidArgumentError` if `listens` is empty or not an array.
       *
       * Note: reconnect attempts continue to follow the configured reconnect
       * policy, but if every entry in the new pool is unreachable the
       * connection may be left unable to recover.
       */
      setServers(listens) {
        if (!Array.isArray(listens) || listens.length === 0) {
          throw errors_1.InvalidArgumentError.format("servers", "cannot be empty");
        }
        const urlParseFn = (0, transport_1.getUrlParseFn)();
        const existing = /* @__PURE__ */ new Map();
        for (const s of this.servers)
          existing.set(s.listen, s);
        const merged = [];
        for (let hp of listens) {
          hp = urlParseFn ? urlParseFn(hp) : hp;
          const { listen } = hostPort(hp);
          const surviving = existing.get(listen);
          if (surviving) {
            surviving.gossiped = false;
            merged.push(surviving);
          } else {
            merged.push(new ServerImpl(hp));
          }
        }
        if (this.randomize)
          (0, util_1.shuffle)(merged);
        this.servers = merged;
        if (this.currentServer === void 0 || !merged.includes(this.currentServer)) {
          this.currentServer = merged[0];
          this.firstSelect = true;
        }
      }
      clear() {
        this.servers.length = 0;
      }
      updateTLSName() {
        const cs = this.getCurrentServer();
        if (!(0, ipparser_1.isIP)(cs.hostname)) {
          this.tlsName = cs.hostname;
          this.servers.forEach((s) => {
            if (s.gossiped) {
              s.tlsName = this.tlsName;
            }
          });
        }
      }
      getCurrentServer() {
        return this.currentServer;
      }
      addServer(u, implicit = false) {
        const urlParseFn = (0, transport_1.getUrlParseFn)();
        u = urlParseFn ? urlParseFn(u) : u;
        const s = new ServerImpl(u, implicit);
        if ((0, ipparser_1.isIP)(s.hostname)) {
          s.tlsName = this.tlsName;
        }
        this.servers.push(s);
      }
      selectServer() {
        if (this.firstSelect) {
          this.firstSelect = false;
          return this.currentServer;
        }
        const t = this.servers.shift();
        if (t) {
          this.servers.push(t);
          this.currentServer = t;
        }
        return t;
      }
      removeCurrentServer() {
        this.removeServer(this.currentServer);
      }
      removeServer(server) {
        if (server) {
          const index = this.servers.indexOf(server);
          this.servers.splice(index, 1);
        }
      }
      /**
       * Returns a frozen snapshot of the server pool in natural order.
       * Each entry is a defensive copy — callers cannot mutate pool state.
       */
      snapshot() {
        return _Servers.freezeAll(this.servers);
      }
      /**
       * Returns a frozen snapshot of the server pool with the current server
       * (= next dial candidate) at index 0. Used to present the handler with
       * the server the library would have selected.
       */
      snapshotForHandler() {
        const cur = this.currentServer;
        if (!cur)
          return this.snapshot();
        const idx = this.servers.indexOf(cur);
        if (idx <= 0)
          return this.snapshot();
        return _Servers.freezeAll([cur, ...this.servers.slice(0, idx), ...this.servers.slice(idx + 1)]);
      }
      static freezeAll(arr) {
        return arr.map((s) => Object.freeze({
          hostname: s.hostname,
          port: s.port,
          listen: s.listen,
          src: s.src,
          tlsName: s.tlsName,
          reconnects: s.reconnects,
          lastConnect: s.lastConnect,
          gossiped: s.gossiped,
          didConnect: s.didConnect
        }));
      }
      find(server) {
        return this.servers.find((s) => s.listen === server.listen);
      }
      setCurrent(server) {
        this.currentServer = server;
      }
      length() {
        return this.servers.length;
      }
      next() {
        return this.servers.length ? this.servers[0] : void 0;
      }
      getServers() {
        return this.servers;
      }
      update(info, encrypted) {
        const added = [];
        let deleted = [];
        const urlParseFn = (0, transport_1.getUrlParseFn)();
        const discovered = /* @__PURE__ */ new Map();
        if (info.connect_urls && info.connect_urls.length > 0) {
          info.connect_urls.forEach((hp) => {
            hp = urlParseFn ? urlParseFn(hp, encrypted) : hp;
            const s = new ServerImpl(hp, true);
            discovered.set(hp, s);
          });
        }
        const toDelete = [];
        this.servers.forEach((s, index) => {
          const u = s.listen;
          if (s.gossiped && this.currentServer.listen !== u && discovered.get(u) === void 0) {
            toDelete.push(index);
          }
          discovered.delete(u);
        });
        toDelete.reverse();
        toDelete.forEach((index) => {
          const removed = this.servers.splice(index, 1);
          deleted = deleted.concat(removed[0].listen);
        });
        discovered.forEach((v, k) => {
          this.servers.push(v);
          added.push(k);
        });
        if (this.randomize && added.length > 0) {
          const cur = this.currentServer;
          const others = this.servers.filter((s) => s !== cur);
          (0, util_1.shuffle)(others);
          this.servers = cur ? [cur, ...others] : others;
        }
        return { added, deleted };
      }
    };
    exports.Servers = Servers;
  }
});

// node_modules/@nats-io/nats-core/lib/queued_iterator.js
var require_queued_iterator = __commonJS({
  "node_modules/@nats-io/nats-core/lib/queued_iterator.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.QueuedIteratorImpl = void 0;
    var util_1 = require_util();
    var errors_1 = require_errors();
    var QueuedIteratorImpl = class {
      inflight;
      processed;
      // this is updated by the protocol
      received;
      noIterator;
      iterClosed;
      done;
      signal;
      yields;
      filtered;
      pendingFiltered;
      ctx;
      _data;
      //data is for use by extenders in any way they like
      err;
      time;
      profile;
      yielding;
      didBreak;
      constructor() {
        this.inflight = 0;
        this.filtered = 0;
        this.pendingFiltered = 0;
        this.processed = 0;
        this.received = 0;
        this.noIterator = false;
        this.done = false;
        this.signal = (0, util_1.deferred)();
        this.yields = [];
        this.iterClosed = (0, util_1.deferred)();
        this.time = 0;
        this.yielding = false;
        this.didBreak = false;
        this.profile = false;
      }
      [Symbol.asyncIterator]() {
        return this.iterate();
      }
      push(v) {
        if (this.done) {
          return;
        }
        if (this.didBreak) {
          if (typeof v === "function") {
            const cb = v;
            try {
              cb();
            } catch (_) {
            }
          }
          return;
        }
        if (typeof v === "function") {
          this.pendingFiltered++;
        }
        this.yields.push(v);
        this.signal.resolve();
      }
      async *iterate() {
        if (this.noIterator) {
          throw new errors_1.InvalidOperationError("iterator cannot be used when a callback is registered");
        }
        if (this.yielding) {
          throw new errors_1.InvalidOperationError("iterator is already yielding");
        }
        this.yielding = true;
        try {
          while (true) {
            if (this.yields.length === 0) {
              await this.signal;
            }
            if (this.err) {
              throw this.err;
            }
            const yields = this.yields;
            this.inflight = yields.length;
            this.yields = [];
            for (let i = 0; i < yields.length; i++) {
              if (typeof yields[i] === "function") {
                this.pendingFiltered--;
                const fn = yields[i];
                try {
                  fn();
                } catch (err) {
                  throw err;
                }
                if (this.err) {
                  throw this.err;
                }
                continue;
              }
              this.processed++;
              this.inflight--;
              const start = this.profile ? Date.now() : 0;
              yield yields[i];
              this.time = this.profile ? Date.now() - start : 0;
            }
            if (this.done) {
              break;
            } else if (this.yields.length === 0) {
              yields.length = 0;
              this.yields = yields;
              this.signal = (0, util_1.deferred)();
            }
          }
        } finally {
          this.didBreak = true;
          this.stop();
        }
      }
      stop(err) {
        if (this.done) {
          return;
        }
        this.err = err;
        this.done = true;
        this.signal.resolve();
        this.iterClosed.resolve(err);
      }
      getProcessed() {
        return this.noIterator ? this.received : this.processed;
      }
      getPending() {
        return this.yields.length + this.inflight - this.pendingFiltered;
      }
      getReceived() {
        return this.received - this.filtered;
      }
    };
    exports.QueuedIteratorImpl = QueuedIteratorImpl;
  }
});

// node_modules/@nats-io/nats-core/lib/muxsubscription.js
var require_muxsubscription = __commonJS({
  "node_modules/@nats-io/nats-core/lib/muxsubscription.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.MuxSubscription = void 0;
    var core_1 = require_core();
    var errors_1 = require_errors();
    var MuxSubscription = class {
      baseInbox;
      reqs;
      constructor() {
        this.reqs = /* @__PURE__ */ new Map();
      }
      size() {
        return this.reqs.size;
      }
      init(prefix) {
        this.baseInbox = `${(0, core_1.createInbox)(prefix)}.`;
        return this.baseInbox;
      }
      add(r) {
        if (!isNaN(r.received)) {
          r.received = 0;
        }
        this.reqs.set(r.token, r);
      }
      get(token) {
        return this.reqs.get(token);
      }
      cancel(r) {
        this.reqs.delete(r.token);
      }
      getToken(m) {
        const s = m.subject || "";
        if (s.indexOf(this.baseInbox) === 0) {
          return s.substring(this.baseInbox.length);
        }
        return null;
      }
      all() {
        return Array.from(this.reqs.values());
      }
      handleError(isMuxPermissionError, err) {
        if (isMuxPermissionError) {
          this.all().forEach((r) => {
            r.resolver(err, {});
          });
          return true;
        }
        if (err.operation === "publish") {
          const req = this.all().find((s) => {
            return s.requestSubject === err.subject;
          });
          if (req) {
            req.resolver(err, {});
            return true;
          }
        }
        return false;
      }
      dispatcher() {
        return (err, m) => {
          const token = this.getToken(m);
          if (token) {
            const r = this.get(token);
            if (r) {
              if (err === null) {
                err = m?.data?.length === 0 && m.headers?.code === 503 ? new errors_1.NoRespondersError(r.requestSubject) : null;
              }
              r.resolver(err, m);
            }
          }
        };
      }
      close() {
        const err = new errors_1.RequestError("connection closed");
        this.reqs.forEach((req) => {
          req.resolver(err, {});
        });
      }
    };
    exports.MuxSubscription = MuxSubscription;
  }
});

// node_modules/@nats-io/nats-core/lib/heartbeats.js
var require_heartbeats = __commonJS({
  "node_modules/@nats-io/nats-core/lib/heartbeats.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.Heartbeat = void 0;
    var util_1 = require_util();
    var Heartbeat = class {
      ph;
      interval;
      maxOut;
      timer;
      pendings;
      constructor(ph, interval, maxOut) {
        this.ph = ph;
        this.interval = interval;
        this.maxOut = maxOut;
        this.pendings = [];
      }
      // api to start the heartbeats, since this can be
      // spuriously called from dial, ensure we don't
      // leak timers
      start() {
        this.cancel();
        this._schedule();
      }
      // api for canceling the heartbeats, if stale is
      // true it will initiate a client disconnect
      cancel(stale) {
        if (this.timer) {
          clearTimeout(this.timer);
          this.timer = void 0;
        }
        this._reset();
        if (stale) {
          this.ph.disconnect();
        }
      }
      _schedule() {
        this.timer = setTimeout(() => {
          this.ph.dispatchStatus({ type: "ping", pendingPings: this.pendings.length + 1 });
          if (this.pendings.length === this.maxOut) {
            this.cancel(true);
            return;
          }
          const ping = (0, util_1.deferred)();
          this.ph.flush(ping).then(() => {
            this._reset();
          }).catch(() => {
            this.cancel();
          });
          this.pendings.push(ping);
          this._schedule();
        }, this.interval);
      }
      _reset() {
        this.pendings = this.pendings.filter((p) => {
          const d = p;
          d.resolve();
          return false;
        });
      }
    };
    exports.Heartbeat = Heartbeat;
  }
});

// node_modules/@nats-io/nats-core/lib/denobuffer.js
var require_denobuffer = __commonJS({
  "node_modules/@nats-io/nats-core/lib/denobuffer.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.DenoBuffer = exports.MAX_SIZE = exports.AssertionError = void 0;
    exports.assert = assert;
    exports.concat = concat;
    exports.append = append;
    exports.readAll = readAll;
    exports.writeAll = writeAll;
    var encoders_1 = require_encoders();
    var AssertionError = class extends Error {
      constructor(msg) {
        super(msg);
        this.name = "AssertionError";
      }
    };
    exports.AssertionError = AssertionError;
    function assert(cond, msg = "Assertion failed.") {
      if (!cond) {
        throw new AssertionError(msg);
      }
    }
    var MIN_READ = 32 * 1024;
    exports.MAX_SIZE = 2 ** 32 - 2;
    function copy(src, dst, off = 0) {
      const r = dst.byteLength - off;
      if (src.byteLength > r) {
        src = src.subarray(0, r);
      }
      dst.set(src, off);
      return src.byteLength;
    }
    function concat(origin, b) {
      if (origin === void 0 && b === void 0) {
        return new Uint8Array(0);
      }
      if (origin === void 0) {
        return b;
      }
      if (b === void 0) {
        return origin;
      }
      const output = new Uint8Array(origin.length + b.length);
      output.set(origin, 0);
      output.set(b, origin.length);
      return output;
    }
    function append(origin, b) {
      return concat(origin, Uint8Array.of(b));
    }
    var DenoBuffer = class {
      _buf;
      // contents are the bytes _buf[off : len(_buf)]
      _off;
      // read at _buf[off], write at _buf[_buf.byteLength]
      constructor(ab) {
        this._off = 0;
        if (ab == null) {
          this._buf = new Uint8Array(0);
          return;
        }
        this._buf = new Uint8Array(ab);
      }
      bytes(options = { copy: true }) {
        if (options.copy === false)
          return this._buf.subarray(this._off);
        return this._buf.slice(this._off);
      }
      empty() {
        return this._buf.byteLength <= this._off;
      }
      get length() {
        return this._buf.byteLength - this._off;
      }
      get capacity() {
        return this._buf.buffer.byteLength;
      }
      truncate(n) {
        if (n === 0) {
          this.reset();
          return;
        }
        if (n < 0 || n > this.length) {
          throw Error("bytes.Buffer: truncation out of range");
        }
        this._reslice(this._off + n);
      }
      reset() {
        this._reslice(0);
        this._off = 0;
      }
      _tryGrowByReslice(n) {
        const l = this._buf.byteLength;
        if (n <= this.capacity - l) {
          this._reslice(l + n);
          return l;
        }
        return -1;
      }
      _reslice(len) {
        assert(len <= this._buf.buffer.byteLength);
        this._buf = new Uint8Array(this._buf.buffer, 0, len);
      }
      readByte() {
        const a = new Uint8Array(1);
        if (this.read(a)) {
          return a[0];
        }
        return null;
      }
      read(p) {
        if (this.empty()) {
          this.reset();
          if (p.byteLength === 0) {
            return 0;
          }
          return null;
        }
        const nread = copy(this._buf.subarray(this._off), p);
        this._off += nread;
        return nread;
      }
      writeByte(n) {
        return this.write(Uint8Array.of(n));
      }
      writeString(s) {
        return this.write(encoders_1.TE.encode(s));
      }
      write(p) {
        const m = this._grow(p.byteLength);
        return copy(p, this._buf, m);
      }
      _grow(n) {
        const m = this.length;
        if (m === 0 && this._off !== 0) {
          this.reset();
        }
        const i = this._tryGrowByReslice(n);
        if (i >= 0) {
          return i;
        }
        const c = this.capacity;
        if (n <= Math.floor(c / 2) - m) {
          copy(this._buf.subarray(this._off), this._buf);
        } else if (c + n > exports.MAX_SIZE) {
          throw new Error("The buffer cannot be grown beyond the maximum size.");
        } else {
          const buf = new Uint8Array(Math.min(2 * c + n, exports.MAX_SIZE));
          copy(this._buf.subarray(this._off), buf);
          this._buf = buf;
        }
        this._off = 0;
        this._reslice(Math.min(m + n, exports.MAX_SIZE));
        return m;
      }
      grow(n) {
        if (n < 0) {
          throw Error("Buffer._grow: negative count");
        }
        const m = this._grow(n);
        this._reslice(m);
      }
      readFrom(r) {
        let n = 0;
        const tmp = new Uint8Array(MIN_READ);
        while (true) {
          const shouldGrow = this.capacity - this.length < MIN_READ;
          const buf = shouldGrow ? tmp : new Uint8Array(this._buf.buffer, this.length);
          const nread = r.read(buf);
          if (nread === null) {
            return n;
          }
          if (shouldGrow)
            this.write(buf.subarray(0, nread));
          else
            this._reslice(this.length + nread);
          n += nread;
        }
      }
    };
    exports.DenoBuffer = DenoBuffer;
    function readAll(r) {
      const buf = new DenoBuffer();
      buf.readFrom(r);
      return buf.bytes();
    }
    function writeAll(w, arr) {
      let nwritten = 0;
      while (nwritten < arr.length) {
        nwritten += w.write(arr.subarray(nwritten));
      }
    }
  }
});

// node_modules/@nats-io/nats-core/lib/parser.js
var require_parser = __commonJS({
  "node_modules/@nats-io/nats-core/lib/parser.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.cc = exports.State = exports.Parser = exports.Kind = void 0;
    exports.describe = describe;
    var denobuffer_1 = require_denobuffer();
    var encoders_1 = require_encoders();
    exports.Kind = {
      OK: 0,
      ERR: 1,
      MSG: 2,
      INFO: 3,
      PING: 4,
      PONG: 5
    };
    function describe(e) {
      let ks;
      let data = "";
      switch (e.kind) {
        case exports.Kind.MSG:
          ks = "MSG";
          break;
        case exports.Kind.OK:
          ks = "OK";
          break;
        case exports.Kind.ERR:
          ks = "ERR";
          data = encoders_1.TD.decode(e.data);
          break;
        case exports.Kind.PING:
          ks = "PING";
          break;
        case exports.Kind.PONG:
          ks = "PONG";
          break;
        case exports.Kind.INFO:
          ks = "INFO";
          data = encoders_1.TD.decode(e.data);
      }
      return `${ks}: ${data}`;
    }
    function newMsgArg() {
      const ma = {};
      ma.sid = -1;
      ma.hdr = -1;
      ma.size = -1;
      return ma;
    }
    var ASCII_0 = 48;
    var ASCII_9 = 57;
    var MAX_64MB = 64 * 1024 * 1024;
    var Parser = class {
      dispatcher;
      state;
      as;
      drop;
      hdr;
      ma;
      argBuf;
      msgBuf;
      constructor(dispatcher) {
        this.dispatcher = dispatcher;
        this.state = exports.State.OP_START;
        this.as = 0;
        this.drop = 0;
        this.hdr = 0;
      }
      parse(buf) {
        let i;
        for (i = 0; i < buf.length; i++) {
          const b = buf[i];
          switch (this.state) {
            case exports.State.OP_START:
              switch (b) {
                case exports.cc.M:
                case exports.cc.m:
                  this.state = exports.State.OP_M;
                  this.hdr = -1;
                  this.ma = newMsgArg();
                  break;
                case exports.cc.H:
                case exports.cc.h:
                  this.state = exports.State.OP_H;
                  this.hdr = 0;
                  this.ma = newMsgArg();
                  break;
                case exports.cc.P:
                case exports.cc.p:
                  this.state = exports.State.OP_P;
                  break;
                case exports.cc.PLUS:
                  this.state = exports.State.OP_PLUS;
                  break;
                case exports.cc.MINUS:
                  this.state = exports.State.OP_MINUS;
                  break;
                case exports.cc.I:
                case exports.cc.i:
                  this.state = exports.State.OP_I;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_H:
              switch (b) {
                case exports.cc.M:
                case exports.cc.m:
                  this.state = exports.State.OP_M;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_M:
              switch (b) {
                case exports.cc.S:
                case exports.cc.s:
                  this.state = exports.State.OP_MS;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_MS:
              switch (b) {
                case exports.cc.G:
                case exports.cc.g:
                  this.state = exports.State.OP_MSG;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_MSG:
              switch (b) {
                case exports.cc.SPACE:
                case exports.cc.TAB:
                  this.state = exports.State.OP_MSG_SPC;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_MSG_SPC:
              switch (b) {
                case exports.cc.SPACE:
                case exports.cc.TAB:
                  continue;
                default:
                  this.state = exports.State.MSG_ARG;
                  this.as = i;
              }
              break;
            case exports.State.MSG_ARG:
              switch (b) {
                case exports.cc.CR:
                  this.drop = 1;
                  break;
                case exports.cc.NL: {
                  const arg = this.argBuf ? this.argBuf.bytes() : buf.subarray(this.as, i - this.drop);
                  this.processMsgArgs(arg);
                  this.drop = 0;
                  this.as = i + 1;
                  this.state = exports.State.MSG_PAYLOAD;
                  i = this.as + this.ma.size - 1;
                  break;
                }
                default:
                  if (this.argBuf) {
                    this.argBuf.writeByte(b);
                  }
              }
              break;
            case exports.State.MSG_PAYLOAD:
              if (this.msgBuf) {
                if (this.msgBuf.length >= this.ma.size) {
                  const data = this.msgBuf.bytes({ copy: false });
                  this.dispatcher.push({ kind: exports.Kind.MSG, msg: this.ma, data });
                  this.argBuf = void 0;
                  this.msgBuf = void 0;
                  this.state = exports.State.MSG_END;
                } else {
                  let toCopy = this.ma.size - this.msgBuf.length;
                  const avail = buf.length - i;
                  if (avail < toCopy) {
                    toCopy = avail;
                  }
                  if (toCopy > 0) {
                    this.msgBuf.write(buf.subarray(i, i + toCopy));
                    i = i + toCopy - 1;
                  } else {
                    this.msgBuf.writeByte(b);
                  }
                }
              } else if (i - this.as >= this.ma.size) {
                this.dispatcher.push({ kind: exports.Kind.MSG, msg: this.ma, data: buf.subarray(this.as, i) });
                this.argBuf = void 0;
                this.msgBuf = void 0;
                this.state = exports.State.MSG_END;
              }
              break;
            case exports.State.MSG_END:
              switch (b) {
                case exports.cc.NL:
                  this.drop = 0;
                  this.as = i + 1;
                  this.state = exports.State.OP_START;
                  break;
                default:
                  continue;
              }
              break;
            case exports.State.OP_PLUS:
              switch (b) {
                case exports.cc.O:
                case exports.cc.o:
                  this.state = exports.State.OP_PLUS_O;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_PLUS_O:
              switch (b) {
                case exports.cc.K:
                case exports.cc.k:
                  this.state = exports.State.OP_PLUS_OK;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_PLUS_OK:
              switch (b) {
                case exports.cc.NL:
                  this.dispatcher.push({ kind: exports.Kind.OK });
                  this.drop = 0;
                  this.state = exports.State.OP_START;
                  break;
              }
              break;
            case exports.State.OP_MINUS:
              switch (b) {
                case exports.cc.E:
                case exports.cc.e:
                  this.state = exports.State.OP_MINUS_E;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_MINUS_E:
              switch (b) {
                case exports.cc.R:
                case exports.cc.r:
                  this.state = exports.State.OP_MINUS_ER;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_MINUS_ER:
              switch (b) {
                case exports.cc.R:
                case exports.cc.r:
                  this.state = exports.State.OP_MINUS_ERR;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_MINUS_ERR:
              switch (b) {
                case exports.cc.SPACE:
                case exports.cc.TAB:
                  this.state = exports.State.OP_MINUS_ERR_SPC;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_MINUS_ERR_SPC:
              switch (b) {
                case exports.cc.SPACE:
                case exports.cc.TAB:
                  continue;
                default:
                  this.state = exports.State.MINUS_ERR_ARG;
                  this.as = i;
              }
              break;
            case exports.State.MINUS_ERR_ARG:
              switch (b) {
                case exports.cc.CR:
                  this.drop = 1;
                  break;
                case exports.cc.NL: {
                  let arg;
                  if (this.argBuf) {
                    arg = this.argBuf.bytes();
                    this.argBuf = void 0;
                  } else {
                    arg = buf.subarray(this.as, i - this.drop);
                  }
                  this.dispatcher.push({ kind: exports.Kind.ERR, data: arg });
                  this.drop = 0;
                  this.as = i + 1;
                  this.state = exports.State.OP_START;
                  break;
                }
                default:
                  if (this.argBuf) {
                    this.argBuf.write(Uint8Array.of(b));
                  }
              }
              break;
            case exports.State.OP_P:
              switch (b) {
                case exports.cc.I:
                case exports.cc.i:
                  this.state = exports.State.OP_PI;
                  break;
                case exports.cc.O:
                case exports.cc.o:
                  this.state = exports.State.OP_PO;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_PO:
              switch (b) {
                case exports.cc.N:
                case exports.cc.n:
                  this.state = exports.State.OP_PON;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_PON:
              switch (b) {
                case exports.cc.G:
                case exports.cc.g:
                  this.state = exports.State.OP_PONG;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_PONG:
              switch (b) {
                case exports.cc.NL:
                  this.dispatcher.push({ kind: exports.Kind.PONG });
                  this.drop = 0;
                  this.state = exports.State.OP_START;
                  break;
              }
              break;
            case exports.State.OP_PI:
              switch (b) {
                case exports.cc.N:
                case exports.cc.n:
                  this.state = exports.State.OP_PIN;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_PIN:
              switch (b) {
                case exports.cc.G:
                case exports.cc.g:
                  this.state = exports.State.OP_PING;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_PING:
              switch (b) {
                case exports.cc.NL:
                  this.dispatcher.push({ kind: exports.Kind.PING });
                  this.drop = 0;
                  this.state = exports.State.OP_START;
                  break;
              }
              break;
            case exports.State.OP_I:
              switch (b) {
                case exports.cc.N:
                case exports.cc.n:
                  this.state = exports.State.OP_IN;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_IN:
              switch (b) {
                case exports.cc.F:
                case exports.cc.f:
                  this.state = exports.State.OP_INF;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_INF:
              switch (b) {
                case exports.cc.O:
                case exports.cc.o:
                  this.state = exports.State.OP_INFO;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_INFO:
              switch (b) {
                case exports.cc.SPACE:
                case exports.cc.TAB:
                  this.state = exports.State.OP_INFO_SPC;
                  break;
                default:
                  throw this.fail(buf.subarray(i));
              }
              break;
            case exports.State.OP_INFO_SPC:
              switch (b) {
                case exports.cc.SPACE:
                case exports.cc.TAB:
                  continue;
                default:
                  this.state = exports.State.INFO_ARG;
                  this.as = i;
              }
              break;
            case exports.State.INFO_ARG:
              switch (b) {
                case exports.cc.CR:
                  this.drop = 1;
                  break;
                case exports.cc.NL: {
                  let arg;
                  if (this.argBuf) {
                    arg = this.argBuf.bytes();
                    this.argBuf = void 0;
                  } else {
                    arg = buf.subarray(this.as, i - this.drop);
                  }
                  this.dispatcher.push({ kind: exports.Kind.INFO, data: arg });
                  this.drop = 0;
                  this.as = i + 1;
                  this.state = exports.State.OP_START;
                  break;
                }
                default:
                  if (this.argBuf) {
                    this.argBuf.writeByte(b);
                  }
              }
              break;
            default:
              throw this.fail(buf.subarray(i));
          }
        }
        if ((this.state === exports.State.MSG_ARG || this.state === exports.State.MINUS_ERR_ARG || this.state === exports.State.INFO_ARG) && !this.argBuf) {
          this.argBuf = new denobuffer_1.DenoBuffer(buf.subarray(this.as, i - this.drop));
        }
        if (this.state === exports.State.MSG_PAYLOAD && !this.msgBuf) {
          if (!this.argBuf) {
            this.cloneMsgArg();
          }
          this.msgBuf = new denobuffer_1.DenoBuffer(buf.subarray(this.as));
        }
      }
      cloneMsgArg() {
        const s = this.ma.subject.length;
        const r = this.ma.reply ? this.ma.reply.length : 0;
        const buf = new Uint8Array(s + r);
        buf.set(this.ma.subject);
        if (this.ma.reply) {
          buf.set(this.ma.reply, s);
        }
        this.argBuf = new denobuffer_1.DenoBuffer(buf);
        this.ma.subject = buf.subarray(0, s);
        if (this.ma.reply) {
          this.ma.reply = buf.subarray(s);
        }
      }
      processMsgArgs(arg) {
        if (this.hdr >= 0) {
          return this.processHeaderMsgArgs(arg);
        }
        const args = [];
        let start = -1;
        for (let i = 0; i < arg.length; i++) {
          const b = arg[i];
          switch (b) {
            case exports.cc.SPACE:
            case exports.cc.TAB:
            case exports.cc.CR:
            case exports.cc.NL:
              if (start >= 0) {
                args.push(arg.subarray(start, i));
                start = -1;
              }
              break;
            default:
              if (start < 0) {
                start = i;
              }
          }
        }
        if (start >= 0) {
          args.push(arg.subarray(start));
        }
        switch (args.length) {
          case 3:
            this.ma.subject = args[0];
            this.ma.sid = this.protoParseInt(args[1]);
            this.ma.reply = void 0;
            this.ma.size = this.protoParseInt(args[2], MAX_64MB);
            break;
          case 4:
            this.ma.subject = args[0];
            this.ma.sid = this.protoParseInt(args[1]);
            this.ma.reply = args[2];
            this.ma.size = this.protoParseInt(args[3], MAX_64MB);
            break;
          default:
            throw this.fail(arg, "processMsgArgs Parse Error");
        }
        if (this.ma.sid < 0) {
          throw this.fail(arg, "processMsgArgs Bad or Missing Sid Error");
        }
        if (this.ma.size < 0) {
          throw this.fail(arg, "processMsgArgs Bad or Missing Size Error");
        }
      }
      fail(data, label = "") {
        if (!label) {
          label = `parse error [${this.state}]`;
        } else {
          label = `${label} [${this.state}]`;
        }
        return new Error(`${label}: ${encoders_1.TD.decode(data)}`);
      }
      processHeaderMsgArgs(arg) {
        const args = [];
        let start = -1;
        for (let i = 0; i < arg.length; i++) {
          const b = arg[i];
          switch (b) {
            case exports.cc.SPACE:
            case exports.cc.TAB:
            case exports.cc.CR:
            case exports.cc.NL:
              if (start >= 0) {
                args.push(arg.subarray(start, i));
                start = -1;
              }
              break;
            default:
              if (start < 0) {
                start = i;
              }
          }
        }
        if (start >= 0) {
          args.push(arg.subarray(start));
        }
        switch (args.length) {
          case 4:
            this.ma.subject = args[0];
            this.ma.sid = this.protoParseInt(args[1]);
            this.ma.reply = void 0;
            this.ma.hdr = this.protoParseInt(args[2], MAX_64MB);
            this.ma.size = this.protoParseInt(args[3], MAX_64MB);
            break;
          case 5:
            this.ma.subject = args[0];
            this.ma.sid = this.protoParseInt(args[1]);
            this.ma.reply = args[2];
            this.ma.hdr = this.protoParseInt(args[3], MAX_64MB);
            this.ma.size = this.protoParseInt(args[4], MAX_64MB);
            break;
          default:
            throw this.fail(arg, "processHeaderMsgArgs Parse Error");
        }
        if (this.ma.sid < 0) {
          throw this.fail(arg, "processHeaderMsgArgs Bad or Missing Sid Error");
        }
        if (this.ma.hdr < 0 || this.ma.hdr > this.ma.size) {
          throw this.fail(arg, "processHeaderMsgArgs Bad or Missing Header Size Error");
        }
        if (this.ma.size < 0) {
          throw this.fail(arg, "processHeaderMsgArgs Bad or Missing Size Error");
        }
      }
      protoParseInt(a, max) {
        if (a.length === 0 || a.length > 15) {
          return -1;
        }
        let n = 0;
        for (let i = 0; i < a.length; i++) {
          if (a[i] < ASCII_0 || a[i] > ASCII_9) {
            return -1;
          }
          n = n * 10 + (a[i] - ASCII_0);
        }
        return max !== void 0 && n > max ? -1 : n;
      }
    };
    exports.Parser = Parser;
    exports.State = {
      OP_START: 0,
      OP_PLUS: 1,
      OP_PLUS_O: 2,
      OP_PLUS_OK: 3,
      OP_MINUS: 4,
      OP_MINUS_E: 5,
      OP_MINUS_ER: 6,
      OP_MINUS_ERR: 7,
      OP_MINUS_ERR_SPC: 8,
      MINUS_ERR_ARG: 9,
      OP_M: 10,
      OP_MS: 11,
      OP_MSG: 12,
      OP_MSG_SPC: 13,
      MSG_ARG: 14,
      MSG_PAYLOAD: 15,
      MSG_END: 16,
      OP_H: 17,
      OP_P: 18,
      OP_PI: 19,
      OP_PIN: 20,
      OP_PING: 21,
      OP_PO: 22,
      OP_PON: 23,
      OP_PONG: 24,
      OP_I: 25,
      OP_IN: 26,
      OP_INF: 27,
      OP_INFO: 28,
      OP_INFO_SPC: 29,
      INFO_ARG: 30
    };
    exports.cc = {
      CR: "\r".charCodeAt(0),
      E: "E".charCodeAt(0),
      e: "e".charCodeAt(0),
      F: "F".charCodeAt(0),
      f: "f".charCodeAt(0),
      G: "G".charCodeAt(0),
      g: "g".charCodeAt(0),
      H: "H".charCodeAt(0),
      h: "h".charCodeAt(0),
      I: "I".charCodeAt(0),
      i: "i".charCodeAt(0),
      K: "K".charCodeAt(0),
      k: "k".charCodeAt(0),
      M: "M".charCodeAt(0),
      m: "m".charCodeAt(0),
      MINUS: "-".charCodeAt(0),
      N: "N".charCodeAt(0),
      n: "n".charCodeAt(0),
      NL: "\n".charCodeAt(0),
      O: "O".charCodeAt(0),
      o: "o".charCodeAt(0),
      P: "P".charCodeAt(0),
      p: "p".charCodeAt(0),
      PLUS: "+".charCodeAt(0),
      R: "R".charCodeAt(0),
      r: "r".charCodeAt(0),
      S: "S".charCodeAt(0),
      s: "s".charCodeAt(0),
      SPACE: " ".charCodeAt(0),
      TAB: "	".charCodeAt(0)
    };
  }
});

// node_modules/@nats-io/nats-core/lib/headers.js
var require_headers = __commonJS({
  "node_modules/@nats-io/nats-core/lib/headers.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.MsgHdrsImpl = void 0;
    exports.canonicalMIMEHeaderKey = canonicalMIMEHeaderKey;
    exports.headers = headers2;
    var encoders_1 = require_encoders();
    var core_1 = require_core();
    var errors_1 = require_errors();
    function canonicalMIMEHeaderKey(k) {
      const a = 97;
      const A = 65;
      const Z = 90;
      const z = 122;
      const dash = 45;
      const colon = 58;
      const start = 33;
      const end = 126;
      const toLower = a - A;
      let upper = true;
      const buf = new Array(k.length);
      for (let i = 0; i < k.length; i++) {
        let c = k.charCodeAt(i);
        if (c === colon || c < start || c > end) {
          throw errors_1.InvalidArgumentError.format("header", `'${k[i]}' is not a valid character in a header name`);
        }
        if (upper && a <= c && c <= z) {
          c -= toLower;
        } else if (!upper && A <= c && c <= Z) {
          c += toLower;
        }
        buf[i] = c;
        upper = c == dash;
      }
      return String.fromCharCode(...buf);
    }
    function headers2(code = 0, description = "") {
      if (code === 0 && description !== "") {
        throw errors_1.InvalidArgumentError.format("code", "is required");
      } else if (code > 0 && description === "") {
        throw errors_1.InvalidArgumentError.format("description", "is required");
      }
      return new MsgHdrsImpl(code, description);
    }
    var HEADER = "NATS/1.0";
    var MsgHdrsImpl = class _MsgHdrsImpl {
      _code;
      headers;
      _description;
      constructor(code = 0, description = "") {
        this._code = code;
        this._description = description;
        this.headers = /* @__PURE__ */ new Map();
      }
      [Symbol.iterator]() {
        return this.headers.entries();
      }
      size() {
        return this.headers.size;
      }
      equals(mh) {
        if (mh && this.headers.size === mh.headers.size && this._code === mh._code) {
          for (const [k, v] of this.headers) {
            const a = mh.values(k);
            if (v.length !== a.length) {
              return false;
            }
            const vv = [...v].sort();
            const aa = [...a].sort();
            for (let i = 0; i < vv.length; i++) {
              if (vv[i] !== aa[i]) {
                return false;
              }
            }
          }
          return true;
        }
        return false;
      }
      static decode(a) {
        const mh = new _MsgHdrsImpl();
        const s = encoders_1.TD.decode(a);
        const lines = s.split("\r\n");
        const h = lines[0];
        if (h !== HEADER) {
          let str = h.replace(HEADER, "").trim();
          if (str.length > 0) {
            mh._code = parseInt(str, 10);
            if (isNaN(mh._code)) {
              mh._code = 0;
            }
            const scode = mh._code.toString();
            str = str.replace(scode, "");
            mh._description = str.trim();
          }
        }
        if (lines.length >= 1) {
          lines.slice(1).map((s2) => {
            if (s2) {
              const idx = s2.indexOf(":");
              if (idx > -1) {
                const k = s2.slice(0, idx);
                const v = s2.slice(idx + 1).trim();
                mh.append(k, v);
              }
            }
          });
        }
        return mh;
      }
      toString() {
        if (this.headers.size === 0 && this._code === 0) {
          return "";
        }
        let s = HEADER;
        if (this._code > 0 && this._description !== "") {
          s += ` ${this._code} ${this._description}`;
        }
        for (const [k, v] of this.headers) {
          for (let i = 0; i < v.length; i++) {
            s = `${s}\r
${k}: ${v[i]}`;
          }
        }
        return `${s}\r
\r
`;
      }
      encode() {
        return encoders_1.TE.encode(this.toString());
      }
      static validHeaderValue(k) {
        const inv = /[\r\n]/;
        if (inv.test(k)) {
          throw errors_1.InvalidArgumentError.format("header", "values cannot contain \\r or \\n");
        }
        return k.trim();
      }
      keys() {
        const keys = [];
        for (const sk of this.headers.keys()) {
          keys.push(sk);
        }
        return keys;
      }
      findKeys(k, match = core_1.Match.Exact) {
        const keys = this.keys();
        switch (match) {
          case core_1.Match.Exact:
            return keys.filter((v) => {
              return v === k;
            });
          case core_1.Match.CanonicalMIME:
            k = canonicalMIMEHeaderKey(k);
            return keys.filter((v) => {
              return v === k;
            });
          default: {
            const lci = k.toLowerCase();
            return keys.filter((v) => {
              return lci === v.toLowerCase();
            });
          }
        }
      }
      get(k, match = core_1.Match.Exact) {
        const keys = this.findKeys(k, match);
        if (keys.length) {
          const v = this.headers.get(keys[0]);
          if (v) {
            return Array.isArray(v) ? v[0] : v;
          }
        }
        return "";
      }
      last(k, match = core_1.Match.Exact) {
        const keys = this.findKeys(k, match);
        if (keys.length) {
          const v = this.headers.get(keys[0]);
          if (v) {
            return Array.isArray(v) ? v[v.length - 1] : v;
          }
        }
        return "";
      }
      has(k, match = core_1.Match.Exact) {
        return this.findKeys(k, match).length > 0;
      }
      set(k, v, match = core_1.Match.Exact) {
        this.delete(k, match);
        this.append(k, v, match);
      }
      append(k, v, match = core_1.Match.Exact) {
        const ck = canonicalMIMEHeaderKey(k);
        if (match === core_1.Match.CanonicalMIME) {
          k = ck;
        }
        const keys = this.findKeys(k, match);
        k = keys.length > 0 ? keys[0] : k;
        const value = _MsgHdrsImpl.validHeaderValue(v);
        let a = this.headers.get(k);
        if (!a) {
          a = [];
          this.headers.set(k, a);
        }
        a.push(value);
      }
      values(k, match = core_1.Match.Exact) {
        const buf = [];
        const keys = this.findKeys(k, match);
        keys.forEach((v) => {
          const values = this.headers.get(v);
          if (values) {
            buf.push(...values);
          }
        });
        return buf;
      }
      delete(k, match = core_1.Match.Exact) {
        const keys = this.findKeys(k, match);
        keys.forEach((v) => {
          this.headers.delete(v);
        });
      }
      get hasError() {
        return this._code >= 300;
      }
      get status() {
        return `${this._code} ${this._description}`.trim();
      }
      toRecord() {
        const data = {};
        this.keys().forEach((v) => {
          data[v] = this.values(v);
        });
        return data;
      }
      get code() {
        return this._code;
      }
      get description() {
        return this._description;
      }
      static fromRecord(r) {
        const h = new _MsgHdrsImpl();
        for (const k in r) {
          const v = r[k];
          h.headers.set(k, Array.isArray(v) ? v : [`${v}`]);
        }
        return h;
      }
    };
    exports.MsgHdrsImpl = MsgHdrsImpl;
  }
});

// node_modules/@nats-io/nats-core/lib/msg.js
var require_msg = __commonJS({
  "node_modules/@nats-io/nats-core/lib/msg.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.MsgImpl = void 0;
    var headers_1 = require_headers();
    var encoders_1 = require_encoders();
    var MsgImpl = class {
      _headers;
      _msg;
      _rdata;
      _reply;
      _subject;
      publisher;
      constructor(msg, data, publisher) {
        this._msg = msg;
        this._rdata = data;
        this.publisher = publisher;
      }
      get subject() {
        if (this._subject) {
          return this._subject;
        }
        this._subject = encoders_1.TD.decode(this._msg.subject);
        return this._subject;
      }
      get reply() {
        if (this._reply) {
          return this._reply;
        }
        this._reply = encoders_1.TD.decode(this._msg.reply);
        return this._reply;
      }
      get sid() {
        return this._msg.sid;
      }
      get headers() {
        if (this._msg.hdr > -1 && !this._headers) {
          const buf = this._rdata.subarray(0, this._msg.hdr);
          this._headers = headers_1.MsgHdrsImpl.decode(buf);
        }
        return this._headers;
      }
      get data() {
        if (!this._rdata) {
          return new Uint8Array(0);
        }
        return this._msg.hdr > -1 ? this._rdata.subarray(this._msg.hdr) : this._rdata;
      }
      // eslint-ignore-next-line @typescript-eslint/no-explicit-any
      respond(data = encoders_1.Empty, opts) {
        if (this.reply) {
          this.publisher.publish(this.reply, data, opts);
          return true;
        }
        return false;
      }
      size() {
        const subj = this._msg.subject.length;
        const reply = this._msg.reply?.length || 0;
        const payloadAndHeaders = this._msg.size === -1 ? 0 : this._msg.size;
        return subj + reply + payloadAndHeaders;
      }
      json(reviver) {
        return JSON.parse(this.string(), reviver);
      }
      string() {
        return encoders_1.TD.decode(this.data);
      }
      requestInfo() {
        const v = this.headers?.get("Nats-Request-Info");
        if (v) {
          return JSON.parse(v, function(key, value) {
            if ((key === "start" || key === "stop") && value !== "") {
              return new Date(Date.parse(value));
            }
            return value;
          });
        }
        return null;
      }
    };
    exports.MsgImpl = MsgImpl;
  }
});

// node_modules/@nats-io/nats-core/lib/semver.js
var require_semver = __commonJS({
  "node_modules/@nats-io/nats-core/lib/semver.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.Features = exports.Feature = void 0;
    exports.parseSemVer = parseSemVer;
    exports.compare = compare;
    function parseSemVer(s = "") {
      const m = s.match(/(\d+).(\d+).(\d+)/);
      if (m) {
        return {
          major: parseInt(m[1]),
          minor: parseInt(m[2]),
          micro: parseInt(m[3])
        };
      }
      throw new Error(`'${s}' is not a semver value`);
    }
    function compare(a, b) {
      if (a.major < b.major)
        return -1;
      if (a.major > b.major)
        return 1;
      if (a.minor < b.minor)
        return -1;
      if (a.minor > b.minor)
        return 1;
      if (a.micro < b.micro)
        return -1;
      if (a.micro > b.micro)
        return 1;
      return 0;
    }
    exports.Feature = {
      JS_KV: "js_kv",
      JS_OBJECTSTORE: "js_objectstore",
      JS_PULL_MAX_BYTES: "js_pull_max_bytes",
      JS_NEW_CONSUMER_CREATE_API: "js_new_consumer_create",
      JS_ALLOW_DIRECT: "js_allow_direct",
      JS_MULTIPLE_CONSUMER_FILTER: "js_multiple_consumer_filter",
      JS_SIMPLIFICATION: "js_simplification",
      JS_STREAM_CONSUMER_METADATA: "js_stream_consumer_metadata",
      JS_CONSUMER_FILTER_SUBJECTS: "js_consumer_filter_subjects",
      JS_STREAM_FIRST_SEQ: "js_stream_first_seq",
      JS_STREAM_SUBJECT_TRANSFORM: "js_stream_subject_transform",
      JS_STREAM_SOURCE_SUBJECT_TRANSFORM: "js_stream_source_subject_transform",
      JS_STREAM_COMPRESSION: "js_stream_compression",
      JS_DEFAULT_CONSUMER_LIMITS: "js_default_consumer_limits",
      JS_BATCH_DIRECT_GET: "js_batch_direct_get",
      JS_PRIORITY_GROUPS: "js_priority_groups",
      JS_CONSUMER_RESET: "js_consumer_reset"
    };
    var Features = class {
      server;
      features;
      disabled;
      constructor(v) {
        this.features = /* @__PURE__ */ new Map();
        this.disabled = [];
        this.update(v);
      }
      /**
       * Removes all disabled entries
       */
      resetDisabled() {
        this.disabled.length = 0;
        this.update(this.server);
      }
      /**
       * Disables a particular feature.
       * @param f
       */
      disable(f) {
        this.disabled.push(f);
        this.update(this.server);
      }
      isDisabled(f) {
        return this.disabled.indexOf(f) !== -1;
      }
      update(v) {
        if (typeof v === "string") {
          v = parseSemVer(v);
        }
        this.server = v;
        this.set(exports.Feature.JS_KV, "2.6.2");
        this.set(exports.Feature.JS_OBJECTSTORE, "2.6.3");
        this.set(exports.Feature.JS_PULL_MAX_BYTES, "2.8.3");
        this.set(exports.Feature.JS_NEW_CONSUMER_CREATE_API, "2.9.0");
        this.set(exports.Feature.JS_ALLOW_DIRECT, "2.9.0");
        this.set(exports.Feature.JS_MULTIPLE_CONSUMER_FILTER, "2.10.0");
        this.set(exports.Feature.JS_SIMPLIFICATION, "2.9.4");
        this.set(exports.Feature.JS_STREAM_CONSUMER_METADATA, "2.10.0");
        this.set(exports.Feature.JS_CONSUMER_FILTER_SUBJECTS, "2.10.0");
        this.set(exports.Feature.JS_STREAM_FIRST_SEQ, "2.10.0");
        this.set(exports.Feature.JS_STREAM_SUBJECT_TRANSFORM, "2.10.0");
        this.set(exports.Feature.JS_STREAM_SOURCE_SUBJECT_TRANSFORM, "2.10.0");
        this.set(exports.Feature.JS_STREAM_COMPRESSION, "2.10.0");
        this.set(exports.Feature.JS_DEFAULT_CONSUMER_LIMITS, "2.10.0");
        this.set(exports.Feature.JS_BATCH_DIRECT_GET, "2.11.0");
        this.set(exports.Feature.JS_PRIORITY_GROUPS, "2.11.0");
        this.set(exports.Feature.JS_CONSUMER_RESET, "2.14.0");
        this.disabled.forEach((f) => {
          this.features.delete(f);
        });
      }
      /**
       * Register a feature that requires a particular server version.
       * @param f
       * @param requires
       */
      set(f, requires) {
        this.features.set(f, {
          min: requires,
          ok: compare(this.server, parseSemVer(requires)) >= 0
        });
      }
      /**
       * Returns whether the feature is available and the min server
       * version that supports it.
       * @param f
       */
      get(f) {
        return this.features.get(f) || { min: "unknown", ok: false };
      }
      /**
       * Returns true if the feature is supported
       * @param f
       */
      supports(f) {
        return this.get(f)?.ok || false;
      }
      /**
       * Returns true if the server is at least the specified version
       * @param v
       */
      require(v) {
        if (typeof v === "string") {
          v = parseSemVer(v);
        }
        return compare(this.server, v) >= 0;
      }
    };
    exports.Features = Features;
  }
});

// node_modules/@nats-io/nkeys/lib/crc16.js
var require_crc16 = __commonJS({
  "node_modules/@nats-io/nkeys/lib/crc16.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.crc16 = void 0;
    var crc16tab = new Uint16Array([
      0,
      4129,
      8258,
      12387,
      16516,
      20645,
      24774,
      28903,
      33032,
      37161,
      41290,
      45419,
      49548,
      53677,
      57806,
      61935,
      4657,
      528,
      12915,
      8786,
      21173,
      17044,
      29431,
      25302,
      37689,
      33560,
      45947,
      41818,
      54205,
      50076,
      62463,
      58334,
      9314,
      13379,
      1056,
      5121,
      25830,
      29895,
      17572,
      21637,
      42346,
      46411,
      34088,
      38153,
      58862,
      62927,
      50604,
      54669,
      13907,
      9842,
      5649,
      1584,
      30423,
      26358,
      22165,
      18100,
      46939,
      42874,
      38681,
      34616,
      63455,
      59390,
      55197,
      51132,
      18628,
      22757,
      26758,
      30887,
      2112,
      6241,
      10242,
      14371,
      51660,
      55789,
      59790,
      63919,
      35144,
      39273,
      43274,
      47403,
      23285,
      19156,
      31415,
      27286,
      6769,
      2640,
      14899,
      10770,
      56317,
      52188,
      64447,
      60318,
      39801,
      35672,
      47931,
      43802,
      27814,
      31879,
      19684,
      23749,
      11298,
      15363,
      3168,
      7233,
      60846,
      64911,
      52716,
      56781,
      44330,
      48395,
      36200,
      40265,
      32407,
      28342,
      24277,
      20212,
      15891,
      11826,
      7761,
      3696,
      65439,
      61374,
      57309,
      53244,
      48923,
      44858,
      40793,
      36728,
      37256,
      33193,
      45514,
      41451,
      53516,
      49453,
      61774,
      57711,
      4224,
      161,
      12482,
      8419,
      20484,
      16421,
      28742,
      24679,
      33721,
      37784,
      41979,
      46042,
      49981,
      54044,
      58239,
      62302,
      689,
      4752,
      8947,
      13010,
      16949,
      21012,
      25207,
      29270,
      46570,
      42443,
      38312,
      34185,
      62830,
      58703,
      54572,
      50445,
      13538,
      9411,
      5280,
      1153,
      29798,
      25671,
      21540,
      17413,
      42971,
      47098,
      34713,
      38840,
      59231,
      63358,
      50973,
      55100,
      9939,
      14066,
      1681,
      5808,
      26199,
      30326,
      17941,
      22068,
      55628,
      51565,
      63758,
      59695,
      39368,
      35305,
      47498,
      43435,
      22596,
      18533,
      30726,
      26663,
      6336,
      2273,
      14466,
      10403,
      52093,
      56156,
      60223,
      64286,
      35833,
      39896,
      43963,
      48026,
      19061,
      23124,
      27191,
      31254,
      2801,
      6864,
      10931,
      14994,
      64814,
      60687,
      56684,
      52557,
      48554,
      44427,
      40424,
      36297,
      31782,
      27655,
      23652,
      19525,
      15522,
      11395,
      7392,
      3265,
      61215,
      65342,
      53085,
      57212,
      44955,
      49082,
      36825,
      40952,
      28183,
      32310,
      20053,
      24180,
      11923,
      16050,
      3793,
      7920
    ]);
    var crc16 = class _crc16 {
      // crc16 returns the crc for the data provided.
      static checksum(data) {
        let crc = 0;
        for (let i = 0; i < data.byteLength; i++) {
          const b = data[i];
          crc = crc << 8 & 65535 ^ crc16tab[(crc >> 8 ^ b) & 255];
        }
        return crc;
      }
      // validate will check the calculated crc16 checksum for data against the expected.
      static validate(data, expected) {
        const ba = _crc16.checksum(data);
        return ba == expected;
      }
    };
    exports.crc16 = crc16;
  }
});

// node_modules/@nats-io/nkeys/lib/base32.js
var require_base32 = __commonJS({
  "node_modules/@nats-io/nkeys/lib/base32.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.base32 = void 0;
    var b32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
    var base32 = class {
      static encode(src) {
        let bits = 0;
        let value = 0;
        const a = new Uint8Array(src);
        const buf = new Uint8Array(src.byteLength * 2);
        let j = 0;
        for (let i = 0; i < a.byteLength; i++) {
          value = value << 8 | a[i];
          bits += 8;
          while (bits >= 5) {
            const index = value >>> bits - 5 & 31;
            buf[j++] = b32Alphabet.charAt(index).charCodeAt(0);
            bits -= 5;
          }
        }
        if (bits > 0) {
          const index = value << 5 - bits & 31;
          buf[j++] = b32Alphabet.charAt(index).charCodeAt(0);
        }
        return buf.slice(0, j);
      }
      static decode(src) {
        let bits = 0;
        let byte = 0;
        let j = 0;
        const a = new Uint8Array(src);
        const out = new Uint8Array(a.byteLength * 5 / 8 | 0);
        for (let i = 0; i < a.byteLength; i++) {
          const v = String.fromCharCode(a[i]);
          const vv = b32Alphabet.indexOf(v);
          if (vv === -1) {
            throw new Error("Illegal Base32 character: " + a[i]);
          }
          byte = byte << 5 | vv;
          bits += 5;
          if (bits >= 8) {
            out[j++] = byte >>> bits - 8 & 255;
            bits -= 8;
          }
        }
        return out.slice(0, j);
      }
    };
    exports.base32 = base32;
  }
});

// node_modules/@nats-io/nkeys/lib/codec.js
var require_codec = __commonJS({
  "node_modules/@nats-io/nkeys/lib/codec.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.Codec = void 0;
    var crc16_1 = require_crc16();
    var nkeys_1 = require_nkeys();
    var base32_1 = require_base32();
    var Codec = class _Codec {
      static encode(prefix, src) {
        if (!src || !(src instanceof Uint8Array)) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.SerializationError);
        }
        if (!nkeys_1.Prefixes.isValidPrefix(prefix)) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidPrefixByte);
        }
        return _Codec._encode(false, prefix, src);
      }
      static encodeSeed(role, src) {
        if (!src) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ApiError);
        }
        if (!nkeys_1.Prefixes.isValidPublicPrefix(role)) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidPrefixByte);
        }
        if (src.byteLength !== 32) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidSeedLen);
        }
        return _Codec._encode(true, role, src);
      }
      static decode(expected, src) {
        if (!nkeys_1.Prefixes.isValidPrefix(expected)) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidPrefixByte);
        }
        const raw = _Codec._decode(src);
        if (raw[0] !== expected) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidPrefixByte);
        }
        return raw.slice(1);
      }
      static decodeSeed(src) {
        const raw = _Codec._decode(src);
        const prefix = _Codec._decodePrefix(raw);
        if (prefix[0] != nkeys_1.Prefix.Seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidSeed);
        }
        if (!nkeys_1.Prefixes.isValidPublicPrefix(prefix[1])) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidPrefixByte);
        }
        return { buf: raw.slice(2), prefix: prefix[1] };
      }
      // unsafe encode no prefix/role validation
      static _encode(seed, role, payload) {
        const payloadOffset = seed ? 2 : 1;
        const payloadLen = payload.byteLength;
        const checkLen = 2;
        const cap = payloadOffset + payloadLen + checkLen;
        const checkOffset = payloadOffset + payloadLen;
        const raw = new Uint8Array(cap);
        if (seed) {
          const encodedPrefix = _Codec._encodePrefix(nkeys_1.Prefix.Seed, role);
          raw.set(encodedPrefix);
        } else {
          raw[0] = role;
        }
        raw.set(payload, payloadOffset);
        const checksum = crc16_1.crc16.checksum(raw.slice(0, checkOffset));
        const dv = new DataView(raw.buffer);
        dv.setUint16(checkOffset, checksum, true);
        return base32_1.base32.encode(raw);
      }
      // unsafe decode - no prefix/role validation
      static _decode(src) {
        if (src.byteLength < 4) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidEncoding);
        }
        let raw;
        try {
          raw = base32_1.base32.decode(src);
        } catch (ex) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidEncoding, { cause: ex });
        }
        const checkOffset = raw.byteLength - 2;
        const dv = new DataView(raw.buffer);
        const checksum = dv.getUint16(checkOffset, true);
        const payload = raw.slice(0, checkOffset);
        if (!crc16_1.crc16.validate(payload, checksum)) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidChecksum);
        }
        return payload;
      }
      static _encodePrefix(kind, role) {
        const b1 = kind | role >> 5;
        const b2 = (role & 31) << 3;
        return new Uint8Array([b1, b2]);
      }
      static _decodePrefix(raw) {
        const b1 = raw[0] & 248;
        const b2 = (raw[0] & 7) << 5 | (raw[1] & 248) >> 3;
        return new Uint8Array([b1, b2]);
      }
    };
    exports.Codec = Codec;
  }
});

// (disabled):crypto
var require_crypto = __commonJS({
  "(disabled):crypto"() {
  }
});

// node_modules/tweetnacl/nacl-fast.js
var require_nacl_fast = __commonJS({
  "node_modules/tweetnacl/nacl-fast.js"(exports, module) {
    (function(nacl) {
      "use strict";
      var gf = function(init) {
        var i, r = new Float64Array(16);
        if (init) for (i = 0; i < init.length; i++) r[i] = init[i];
        return r;
      };
      var randombytes = function() {
        throw new Error("no PRNG");
      };
      var _0 = new Uint8Array(16);
      var _9 = new Uint8Array(32);
      _9[0] = 9;
      var gf0 = gf(), gf1 = gf([1]), _121665 = gf([56129, 1]), D = gf([30883, 4953, 19914, 30187, 55467, 16705, 2637, 112, 59544, 30585, 16505, 36039, 65139, 11119, 27886, 20995]), D2 = gf([61785, 9906, 39828, 60374, 45398, 33411, 5274, 224, 53552, 61171, 33010, 6542, 64743, 22239, 55772, 9222]), X = gf([54554, 36645, 11616, 51542, 42930, 38181, 51040, 26924, 56412, 64982, 57905, 49316, 21502, 52590, 14035, 8553]), Y = gf([26200, 26214, 26214, 26214, 26214, 26214, 26214, 26214, 26214, 26214, 26214, 26214, 26214, 26214, 26214, 26214]), I = gf([41136, 18958, 6951, 50414, 58488, 44335, 6150, 12099, 55207, 15867, 153, 11085, 57099, 20417, 9344, 11139]);
      function ts64(x, i, h, l) {
        x[i] = h >> 24 & 255;
        x[i + 1] = h >> 16 & 255;
        x[i + 2] = h >> 8 & 255;
        x[i + 3] = h & 255;
        x[i + 4] = l >> 24 & 255;
        x[i + 5] = l >> 16 & 255;
        x[i + 6] = l >> 8 & 255;
        x[i + 7] = l & 255;
      }
      function vn(x, xi, y, yi, n) {
        var i, d = 0;
        for (i = 0; i < n; i++) d |= x[xi + i] ^ y[yi + i];
        return (1 & d - 1 >>> 8) - 1;
      }
      function crypto_verify_16(x, xi, y, yi) {
        return vn(x, xi, y, yi, 16);
      }
      function crypto_verify_32(x, xi, y, yi) {
        return vn(x, xi, y, yi, 32);
      }
      function core_salsa20(o, p, k, c) {
        var j0 = c[0] & 255 | (c[1] & 255) << 8 | (c[2] & 255) << 16 | (c[3] & 255) << 24, j1 = k[0] & 255 | (k[1] & 255) << 8 | (k[2] & 255) << 16 | (k[3] & 255) << 24, j2 = k[4] & 255 | (k[5] & 255) << 8 | (k[6] & 255) << 16 | (k[7] & 255) << 24, j3 = k[8] & 255 | (k[9] & 255) << 8 | (k[10] & 255) << 16 | (k[11] & 255) << 24, j4 = k[12] & 255 | (k[13] & 255) << 8 | (k[14] & 255) << 16 | (k[15] & 255) << 24, j5 = c[4] & 255 | (c[5] & 255) << 8 | (c[6] & 255) << 16 | (c[7] & 255) << 24, j6 = p[0] & 255 | (p[1] & 255) << 8 | (p[2] & 255) << 16 | (p[3] & 255) << 24, j7 = p[4] & 255 | (p[5] & 255) << 8 | (p[6] & 255) << 16 | (p[7] & 255) << 24, j8 = p[8] & 255 | (p[9] & 255) << 8 | (p[10] & 255) << 16 | (p[11] & 255) << 24, j9 = p[12] & 255 | (p[13] & 255) << 8 | (p[14] & 255) << 16 | (p[15] & 255) << 24, j10 = c[8] & 255 | (c[9] & 255) << 8 | (c[10] & 255) << 16 | (c[11] & 255) << 24, j11 = k[16] & 255 | (k[17] & 255) << 8 | (k[18] & 255) << 16 | (k[19] & 255) << 24, j12 = k[20] & 255 | (k[21] & 255) << 8 | (k[22] & 255) << 16 | (k[23] & 255) << 24, j13 = k[24] & 255 | (k[25] & 255) << 8 | (k[26] & 255) << 16 | (k[27] & 255) << 24, j14 = k[28] & 255 | (k[29] & 255) << 8 | (k[30] & 255) << 16 | (k[31] & 255) << 24, j15 = c[12] & 255 | (c[13] & 255) << 8 | (c[14] & 255) << 16 | (c[15] & 255) << 24;
        var x0 = j0, x1 = j1, x2 = j2, x3 = j3, x4 = j4, x5 = j5, x6 = j6, x7 = j7, x8 = j8, x9 = j9, x10 = j10, x11 = j11, x12 = j12, x13 = j13, x14 = j14, x15 = j15, u;
        for (var i = 0; i < 20; i += 2) {
          u = x0 + x12 | 0;
          x4 ^= u << 7 | u >>> 32 - 7;
          u = x4 + x0 | 0;
          x8 ^= u << 9 | u >>> 32 - 9;
          u = x8 + x4 | 0;
          x12 ^= u << 13 | u >>> 32 - 13;
          u = x12 + x8 | 0;
          x0 ^= u << 18 | u >>> 32 - 18;
          u = x5 + x1 | 0;
          x9 ^= u << 7 | u >>> 32 - 7;
          u = x9 + x5 | 0;
          x13 ^= u << 9 | u >>> 32 - 9;
          u = x13 + x9 | 0;
          x1 ^= u << 13 | u >>> 32 - 13;
          u = x1 + x13 | 0;
          x5 ^= u << 18 | u >>> 32 - 18;
          u = x10 + x6 | 0;
          x14 ^= u << 7 | u >>> 32 - 7;
          u = x14 + x10 | 0;
          x2 ^= u << 9 | u >>> 32 - 9;
          u = x2 + x14 | 0;
          x6 ^= u << 13 | u >>> 32 - 13;
          u = x6 + x2 | 0;
          x10 ^= u << 18 | u >>> 32 - 18;
          u = x15 + x11 | 0;
          x3 ^= u << 7 | u >>> 32 - 7;
          u = x3 + x15 | 0;
          x7 ^= u << 9 | u >>> 32 - 9;
          u = x7 + x3 | 0;
          x11 ^= u << 13 | u >>> 32 - 13;
          u = x11 + x7 | 0;
          x15 ^= u << 18 | u >>> 32 - 18;
          u = x0 + x3 | 0;
          x1 ^= u << 7 | u >>> 32 - 7;
          u = x1 + x0 | 0;
          x2 ^= u << 9 | u >>> 32 - 9;
          u = x2 + x1 | 0;
          x3 ^= u << 13 | u >>> 32 - 13;
          u = x3 + x2 | 0;
          x0 ^= u << 18 | u >>> 32 - 18;
          u = x5 + x4 | 0;
          x6 ^= u << 7 | u >>> 32 - 7;
          u = x6 + x5 | 0;
          x7 ^= u << 9 | u >>> 32 - 9;
          u = x7 + x6 | 0;
          x4 ^= u << 13 | u >>> 32 - 13;
          u = x4 + x7 | 0;
          x5 ^= u << 18 | u >>> 32 - 18;
          u = x10 + x9 | 0;
          x11 ^= u << 7 | u >>> 32 - 7;
          u = x11 + x10 | 0;
          x8 ^= u << 9 | u >>> 32 - 9;
          u = x8 + x11 | 0;
          x9 ^= u << 13 | u >>> 32 - 13;
          u = x9 + x8 | 0;
          x10 ^= u << 18 | u >>> 32 - 18;
          u = x15 + x14 | 0;
          x12 ^= u << 7 | u >>> 32 - 7;
          u = x12 + x15 | 0;
          x13 ^= u << 9 | u >>> 32 - 9;
          u = x13 + x12 | 0;
          x14 ^= u << 13 | u >>> 32 - 13;
          u = x14 + x13 | 0;
          x15 ^= u << 18 | u >>> 32 - 18;
        }
        x0 = x0 + j0 | 0;
        x1 = x1 + j1 | 0;
        x2 = x2 + j2 | 0;
        x3 = x3 + j3 | 0;
        x4 = x4 + j4 | 0;
        x5 = x5 + j5 | 0;
        x6 = x6 + j6 | 0;
        x7 = x7 + j7 | 0;
        x8 = x8 + j8 | 0;
        x9 = x9 + j9 | 0;
        x10 = x10 + j10 | 0;
        x11 = x11 + j11 | 0;
        x12 = x12 + j12 | 0;
        x13 = x13 + j13 | 0;
        x14 = x14 + j14 | 0;
        x15 = x15 + j15 | 0;
        o[0] = x0 >>> 0 & 255;
        o[1] = x0 >>> 8 & 255;
        o[2] = x0 >>> 16 & 255;
        o[3] = x0 >>> 24 & 255;
        o[4] = x1 >>> 0 & 255;
        o[5] = x1 >>> 8 & 255;
        o[6] = x1 >>> 16 & 255;
        o[7] = x1 >>> 24 & 255;
        o[8] = x2 >>> 0 & 255;
        o[9] = x2 >>> 8 & 255;
        o[10] = x2 >>> 16 & 255;
        o[11] = x2 >>> 24 & 255;
        o[12] = x3 >>> 0 & 255;
        o[13] = x3 >>> 8 & 255;
        o[14] = x3 >>> 16 & 255;
        o[15] = x3 >>> 24 & 255;
        o[16] = x4 >>> 0 & 255;
        o[17] = x4 >>> 8 & 255;
        o[18] = x4 >>> 16 & 255;
        o[19] = x4 >>> 24 & 255;
        o[20] = x5 >>> 0 & 255;
        o[21] = x5 >>> 8 & 255;
        o[22] = x5 >>> 16 & 255;
        o[23] = x5 >>> 24 & 255;
        o[24] = x6 >>> 0 & 255;
        o[25] = x6 >>> 8 & 255;
        o[26] = x6 >>> 16 & 255;
        o[27] = x6 >>> 24 & 255;
        o[28] = x7 >>> 0 & 255;
        o[29] = x7 >>> 8 & 255;
        o[30] = x7 >>> 16 & 255;
        o[31] = x7 >>> 24 & 255;
        o[32] = x8 >>> 0 & 255;
        o[33] = x8 >>> 8 & 255;
        o[34] = x8 >>> 16 & 255;
        o[35] = x8 >>> 24 & 255;
        o[36] = x9 >>> 0 & 255;
        o[37] = x9 >>> 8 & 255;
        o[38] = x9 >>> 16 & 255;
        o[39] = x9 >>> 24 & 255;
        o[40] = x10 >>> 0 & 255;
        o[41] = x10 >>> 8 & 255;
        o[42] = x10 >>> 16 & 255;
        o[43] = x10 >>> 24 & 255;
        o[44] = x11 >>> 0 & 255;
        o[45] = x11 >>> 8 & 255;
        o[46] = x11 >>> 16 & 255;
        o[47] = x11 >>> 24 & 255;
        o[48] = x12 >>> 0 & 255;
        o[49] = x12 >>> 8 & 255;
        o[50] = x12 >>> 16 & 255;
        o[51] = x12 >>> 24 & 255;
        o[52] = x13 >>> 0 & 255;
        o[53] = x13 >>> 8 & 255;
        o[54] = x13 >>> 16 & 255;
        o[55] = x13 >>> 24 & 255;
        o[56] = x14 >>> 0 & 255;
        o[57] = x14 >>> 8 & 255;
        o[58] = x14 >>> 16 & 255;
        o[59] = x14 >>> 24 & 255;
        o[60] = x15 >>> 0 & 255;
        o[61] = x15 >>> 8 & 255;
        o[62] = x15 >>> 16 & 255;
        o[63] = x15 >>> 24 & 255;
      }
      function core_hsalsa20(o, p, k, c) {
        var j0 = c[0] & 255 | (c[1] & 255) << 8 | (c[2] & 255) << 16 | (c[3] & 255) << 24, j1 = k[0] & 255 | (k[1] & 255) << 8 | (k[2] & 255) << 16 | (k[3] & 255) << 24, j2 = k[4] & 255 | (k[5] & 255) << 8 | (k[6] & 255) << 16 | (k[7] & 255) << 24, j3 = k[8] & 255 | (k[9] & 255) << 8 | (k[10] & 255) << 16 | (k[11] & 255) << 24, j4 = k[12] & 255 | (k[13] & 255) << 8 | (k[14] & 255) << 16 | (k[15] & 255) << 24, j5 = c[4] & 255 | (c[5] & 255) << 8 | (c[6] & 255) << 16 | (c[7] & 255) << 24, j6 = p[0] & 255 | (p[1] & 255) << 8 | (p[2] & 255) << 16 | (p[3] & 255) << 24, j7 = p[4] & 255 | (p[5] & 255) << 8 | (p[6] & 255) << 16 | (p[7] & 255) << 24, j8 = p[8] & 255 | (p[9] & 255) << 8 | (p[10] & 255) << 16 | (p[11] & 255) << 24, j9 = p[12] & 255 | (p[13] & 255) << 8 | (p[14] & 255) << 16 | (p[15] & 255) << 24, j10 = c[8] & 255 | (c[9] & 255) << 8 | (c[10] & 255) << 16 | (c[11] & 255) << 24, j11 = k[16] & 255 | (k[17] & 255) << 8 | (k[18] & 255) << 16 | (k[19] & 255) << 24, j12 = k[20] & 255 | (k[21] & 255) << 8 | (k[22] & 255) << 16 | (k[23] & 255) << 24, j13 = k[24] & 255 | (k[25] & 255) << 8 | (k[26] & 255) << 16 | (k[27] & 255) << 24, j14 = k[28] & 255 | (k[29] & 255) << 8 | (k[30] & 255) << 16 | (k[31] & 255) << 24, j15 = c[12] & 255 | (c[13] & 255) << 8 | (c[14] & 255) << 16 | (c[15] & 255) << 24;
        var x0 = j0, x1 = j1, x2 = j2, x3 = j3, x4 = j4, x5 = j5, x6 = j6, x7 = j7, x8 = j8, x9 = j9, x10 = j10, x11 = j11, x12 = j12, x13 = j13, x14 = j14, x15 = j15, u;
        for (var i = 0; i < 20; i += 2) {
          u = x0 + x12 | 0;
          x4 ^= u << 7 | u >>> 32 - 7;
          u = x4 + x0 | 0;
          x8 ^= u << 9 | u >>> 32 - 9;
          u = x8 + x4 | 0;
          x12 ^= u << 13 | u >>> 32 - 13;
          u = x12 + x8 | 0;
          x0 ^= u << 18 | u >>> 32 - 18;
          u = x5 + x1 | 0;
          x9 ^= u << 7 | u >>> 32 - 7;
          u = x9 + x5 | 0;
          x13 ^= u << 9 | u >>> 32 - 9;
          u = x13 + x9 | 0;
          x1 ^= u << 13 | u >>> 32 - 13;
          u = x1 + x13 | 0;
          x5 ^= u << 18 | u >>> 32 - 18;
          u = x10 + x6 | 0;
          x14 ^= u << 7 | u >>> 32 - 7;
          u = x14 + x10 | 0;
          x2 ^= u << 9 | u >>> 32 - 9;
          u = x2 + x14 | 0;
          x6 ^= u << 13 | u >>> 32 - 13;
          u = x6 + x2 | 0;
          x10 ^= u << 18 | u >>> 32 - 18;
          u = x15 + x11 | 0;
          x3 ^= u << 7 | u >>> 32 - 7;
          u = x3 + x15 | 0;
          x7 ^= u << 9 | u >>> 32 - 9;
          u = x7 + x3 | 0;
          x11 ^= u << 13 | u >>> 32 - 13;
          u = x11 + x7 | 0;
          x15 ^= u << 18 | u >>> 32 - 18;
          u = x0 + x3 | 0;
          x1 ^= u << 7 | u >>> 32 - 7;
          u = x1 + x0 | 0;
          x2 ^= u << 9 | u >>> 32 - 9;
          u = x2 + x1 | 0;
          x3 ^= u << 13 | u >>> 32 - 13;
          u = x3 + x2 | 0;
          x0 ^= u << 18 | u >>> 32 - 18;
          u = x5 + x4 | 0;
          x6 ^= u << 7 | u >>> 32 - 7;
          u = x6 + x5 | 0;
          x7 ^= u << 9 | u >>> 32 - 9;
          u = x7 + x6 | 0;
          x4 ^= u << 13 | u >>> 32 - 13;
          u = x4 + x7 | 0;
          x5 ^= u << 18 | u >>> 32 - 18;
          u = x10 + x9 | 0;
          x11 ^= u << 7 | u >>> 32 - 7;
          u = x11 + x10 | 0;
          x8 ^= u << 9 | u >>> 32 - 9;
          u = x8 + x11 | 0;
          x9 ^= u << 13 | u >>> 32 - 13;
          u = x9 + x8 | 0;
          x10 ^= u << 18 | u >>> 32 - 18;
          u = x15 + x14 | 0;
          x12 ^= u << 7 | u >>> 32 - 7;
          u = x12 + x15 | 0;
          x13 ^= u << 9 | u >>> 32 - 9;
          u = x13 + x12 | 0;
          x14 ^= u << 13 | u >>> 32 - 13;
          u = x14 + x13 | 0;
          x15 ^= u << 18 | u >>> 32 - 18;
        }
        o[0] = x0 >>> 0 & 255;
        o[1] = x0 >>> 8 & 255;
        o[2] = x0 >>> 16 & 255;
        o[3] = x0 >>> 24 & 255;
        o[4] = x5 >>> 0 & 255;
        o[5] = x5 >>> 8 & 255;
        o[6] = x5 >>> 16 & 255;
        o[7] = x5 >>> 24 & 255;
        o[8] = x10 >>> 0 & 255;
        o[9] = x10 >>> 8 & 255;
        o[10] = x10 >>> 16 & 255;
        o[11] = x10 >>> 24 & 255;
        o[12] = x15 >>> 0 & 255;
        o[13] = x15 >>> 8 & 255;
        o[14] = x15 >>> 16 & 255;
        o[15] = x15 >>> 24 & 255;
        o[16] = x6 >>> 0 & 255;
        o[17] = x6 >>> 8 & 255;
        o[18] = x6 >>> 16 & 255;
        o[19] = x6 >>> 24 & 255;
        o[20] = x7 >>> 0 & 255;
        o[21] = x7 >>> 8 & 255;
        o[22] = x7 >>> 16 & 255;
        o[23] = x7 >>> 24 & 255;
        o[24] = x8 >>> 0 & 255;
        o[25] = x8 >>> 8 & 255;
        o[26] = x8 >>> 16 & 255;
        o[27] = x8 >>> 24 & 255;
        o[28] = x9 >>> 0 & 255;
        o[29] = x9 >>> 8 & 255;
        o[30] = x9 >>> 16 & 255;
        o[31] = x9 >>> 24 & 255;
      }
      function crypto_core_salsa20(out, inp, k, c) {
        core_salsa20(out, inp, k, c);
      }
      function crypto_core_hsalsa20(out, inp, k, c) {
        core_hsalsa20(out, inp, k, c);
      }
      var sigma = new Uint8Array([101, 120, 112, 97, 110, 100, 32, 51, 50, 45, 98, 121, 116, 101, 32, 107]);
      function crypto_stream_salsa20_xor(c, cpos, m, mpos, b, n, k) {
        var z = new Uint8Array(16), x = new Uint8Array(64);
        var u, i;
        for (i = 0; i < 16; i++) z[i] = 0;
        for (i = 0; i < 8; i++) z[i] = n[i];
        while (b >= 64) {
          crypto_core_salsa20(x, z, k, sigma);
          for (i = 0; i < 64; i++) c[cpos + i] = m[mpos + i] ^ x[i];
          u = 1;
          for (i = 8; i < 16; i++) {
            u = u + (z[i] & 255) | 0;
            z[i] = u & 255;
            u >>>= 8;
          }
          b -= 64;
          cpos += 64;
          mpos += 64;
        }
        if (b > 0) {
          crypto_core_salsa20(x, z, k, sigma);
          for (i = 0; i < b; i++) c[cpos + i] = m[mpos + i] ^ x[i];
        }
        return 0;
      }
      function crypto_stream_salsa20(c, cpos, b, n, k) {
        var z = new Uint8Array(16), x = new Uint8Array(64);
        var u, i;
        for (i = 0; i < 16; i++) z[i] = 0;
        for (i = 0; i < 8; i++) z[i] = n[i];
        while (b >= 64) {
          crypto_core_salsa20(x, z, k, sigma);
          for (i = 0; i < 64; i++) c[cpos + i] = x[i];
          u = 1;
          for (i = 8; i < 16; i++) {
            u = u + (z[i] & 255) | 0;
            z[i] = u & 255;
            u >>>= 8;
          }
          b -= 64;
          cpos += 64;
        }
        if (b > 0) {
          crypto_core_salsa20(x, z, k, sigma);
          for (i = 0; i < b; i++) c[cpos + i] = x[i];
        }
        return 0;
      }
      function crypto_stream(c, cpos, d, n, k) {
        var s = new Uint8Array(32);
        crypto_core_hsalsa20(s, n, k, sigma);
        var sn = new Uint8Array(8);
        for (var i = 0; i < 8; i++) sn[i] = n[i + 16];
        return crypto_stream_salsa20(c, cpos, d, sn, s);
      }
      function crypto_stream_xor(c, cpos, m, mpos, d, n, k) {
        var s = new Uint8Array(32);
        crypto_core_hsalsa20(s, n, k, sigma);
        var sn = new Uint8Array(8);
        for (var i = 0; i < 8; i++) sn[i] = n[i + 16];
        return crypto_stream_salsa20_xor(c, cpos, m, mpos, d, sn, s);
      }
      var poly1305 = function(key) {
        this.buffer = new Uint8Array(16);
        this.r = new Uint16Array(10);
        this.h = new Uint16Array(10);
        this.pad = new Uint16Array(8);
        this.leftover = 0;
        this.fin = 0;
        var t0, t1, t2, t3, t4, t5, t6, t7;
        t0 = key[0] & 255 | (key[1] & 255) << 8;
        this.r[0] = t0 & 8191;
        t1 = key[2] & 255 | (key[3] & 255) << 8;
        this.r[1] = (t0 >>> 13 | t1 << 3) & 8191;
        t2 = key[4] & 255 | (key[5] & 255) << 8;
        this.r[2] = (t1 >>> 10 | t2 << 6) & 7939;
        t3 = key[6] & 255 | (key[7] & 255) << 8;
        this.r[3] = (t2 >>> 7 | t3 << 9) & 8191;
        t4 = key[8] & 255 | (key[9] & 255) << 8;
        this.r[4] = (t3 >>> 4 | t4 << 12) & 255;
        this.r[5] = t4 >>> 1 & 8190;
        t5 = key[10] & 255 | (key[11] & 255) << 8;
        this.r[6] = (t4 >>> 14 | t5 << 2) & 8191;
        t6 = key[12] & 255 | (key[13] & 255) << 8;
        this.r[7] = (t5 >>> 11 | t6 << 5) & 8065;
        t7 = key[14] & 255 | (key[15] & 255) << 8;
        this.r[8] = (t6 >>> 8 | t7 << 8) & 8191;
        this.r[9] = t7 >>> 5 & 127;
        this.pad[0] = key[16] & 255 | (key[17] & 255) << 8;
        this.pad[1] = key[18] & 255 | (key[19] & 255) << 8;
        this.pad[2] = key[20] & 255 | (key[21] & 255) << 8;
        this.pad[3] = key[22] & 255 | (key[23] & 255) << 8;
        this.pad[4] = key[24] & 255 | (key[25] & 255) << 8;
        this.pad[5] = key[26] & 255 | (key[27] & 255) << 8;
        this.pad[6] = key[28] & 255 | (key[29] & 255) << 8;
        this.pad[7] = key[30] & 255 | (key[31] & 255) << 8;
      };
      poly1305.prototype.blocks = function(m, mpos, bytes) {
        var hibit = this.fin ? 0 : 1 << 11;
        var t0, t1, t2, t3, t4, t5, t6, t7, c;
        var d0, d1, d2, d3, d4, d5, d6, d7, d8, d9;
        var h0 = this.h[0], h1 = this.h[1], h2 = this.h[2], h3 = this.h[3], h4 = this.h[4], h5 = this.h[5], h6 = this.h[6], h7 = this.h[7], h8 = this.h[8], h9 = this.h[9];
        var r0 = this.r[0], r1 = this.r[1], r2 = this.r[2], r3 = this.r[3], r4 = this.r[4], r5 = this.r[5], r6 = this.r[6], r7 = this.r[7], r8 = this.r[8], r9 = this.r[9];
        while (bytes >= 16) {
          t0 = m[mpos + 0] & 255 | (m[mpos + 1] & 255) << 8;
          h0 += t0 & 8191;
          t1 = m[mpos + 2] & 255 | (m[mpos + 3] & 255) << 8;
          h1 += (t0 >>> 13 | t1 << 3) & 8191;
          t2 = m[mpos + 4] & 255 | (m[mpos + 5] & 255) << 8;
          h2 += (t1 >>> 10 | t2 << 6) & 8191;
          t3 = m[mpos + 6] & 255 | (m[mpos + 7] & 255) << 8;
          h3 += (t2 >>> 7 | t3 << 9) & 8191;
          t4 = m[mpos + 8] & 255 | (m[mpos + 9] & 255) << 8;
          h4 += (t3 >>> 4 | t4 << 12) & 8191;
          h5 += t4 >>> 1 & 8191;
          t5 = m[mpos + 10] & 255 | (m[mpos + 11] & 255) << 8;
          h6 += (t4 >>> 14 | t5 << 2) & 8191;
          t6 = m[mpos + 12] & 255 | (m[mpos + 13] & 255) << 8;
          h7 += (t5 >>> 11 | t6 << 5) & 8191;
          t7 = m[mpos + 14] & 255 | (m[mpos + 15] & 255) << 8;
          h8 += (t6 >>> 8 | t7 << 8) & 8191;
          h9 += t7 >>> 5 | hibit;
          c = 0;
          d0 = c;
          d0 += h0 * r0;
          d0 += h1 * (5 * r9);
          d0 += h2 * (5 * r8);
          d0 += h3 * (5 * r7);
          d0 += h4 * (5 * r6);
          c = d0 >>> 13;
          d0 &= 8191;
          d0 += h5 * (5 * r5);
          d0 += h6 * (5 * r4);
          d0 += h7 * (5 * r3);
          d0 += h8 * (5 * r2);
          d0 += h9 * (5 * r1);
          c += d0 >>> 13;
          d0 &= 8191;
          d1 = c;
          d1 += h0 * r1;
          d1 += h1 * r0;
          d1 += h2 * (5 * r9);
          d1 += h3 * (5 * r8);
          d1 += h4 * (5 * r7);
          c = d1 >>> 13;
          d1 &= 8191;
          d1 += h5 * (5 * r6);
          d1 += h6 * (5 * r5);
          d1 += h7 * (5 * r4);
          d1 += h8 * (5 * r3);
          d1 += h9 * (5 * r2);
          c += d1 >>> 13;
          d1 &= 8191;
          d2 = c;
          d2 += h0 * r2;
          d2 += h1 * r1;
          d2 += h2 * r0;
          d2 += h3 * (5 * r9);
          d2 += h4 * (5 * r8);
          c = d2 >>> 13;
          d2 &= 8191;
          d2 += h5 * (5 * r7);
          d2 += h6 * (5 * r6);
          d2 += h7 * (5 * r5);
          d2 += h8 * (5 * r4);
          d2 += h9 * (5 * r3);
          c += d2 >>> 13;
          d2 &= 8191;
          d3 = c;
          d3 += h0 * r3;
          d3 += h1 * r2;
          d3 += h2 * r1;
          d3 += h3 * r0;
          d3 += h4 * (5 * r9);
          c = d3 >>> 13;
          d3 &= 8191;
          d3 += h5 * (5 * r8);
          d3 += h6 * (5 * r7);
          d3 += h7 * (5 * r6);
          d3 += h8 * (5 * r5);
          d3 += h9 * (5 * r4);
          c += d3 >>> 13;
          d3 &= 8191;
          d4 = c;
          d4 += h0 * r4;
          d4 += h1 * r3;
          d4 += h2 * r2;
          d4 += h3 * r1;
          d4 += h4 * r0;
          c = d4 >>> 13;
          d4 &= 8191;
          d4 += h5 * (5 * r9);
          d4 += h6 * (5 * r8);
          d4 += h7 * (5 * r7);
          d4 += h8 * (5 * r6);
          d4 += h9 * (5 * r5);
          c += d4 >>> 13;
          d4 &= 8191;
          d5 = c;
          d5 += h0 * r5;
          d5 += h1 * r4;
          d5 += h2 * r3;
          d5 += h3 * r2;
          d5 += h4 * r1;
          c = d5 >>> 13;
          d5 &= 8191;
          d5 += h5 * r0;
          d5 += h6 * (5 * r9);
          d5 += h7 * (5 * r8);
          d5 += h8 * (5 * r7);
          d5 += h9 * (5 * r6);
          c += d5 >>> 13;
          d5 &= 8191;
          d6 = c;
          d6 += h0 * r6;
          d6 += h1 * r5;
          d6 += h2 * r4;
          d6 += h3 * r3;
          d6 += h4 * r2;
          c = d6 >>> 13;
          d6 &= 8191;
          d6 += h5 * r1;
          d6 += h6 * r0;
          d6 += h7 * (5 * r9);
          d6 += h8 * (5 * r8);
          d6 += h9 * (5 * r7);
          c += d6 >>> 13;
          d6 &= 8191;
          d7 = c;
          d7 += h0 * r7;
          d7 += h1 * r6;
          d7 += h2 * r5;
          d7 += h3 * r4;
          d7 += h4 * r3;
          c = d7 >>> 13;
          d7 &= 8191;
          d7 += h5 * r2;
          d7 += h6 * r1;
          d7 += h7 * r0;
          d7 += h8 * (5 * r9);
          d7 += h9 * (5 * r8);
          c += d7 >>> 13;
          d7 &= 8191;
          d8 = c;
          d8 += h0 * r8;
          d8 += h1 * r7;
          d8 += h2 * r6;
          d8 += h3 * r5;
          d8 += h4 * r4;
          c = d8 >>> 13;
          d8 &= 8191;
          d8 += h5 * r3;
          d8 += h6 * r2;
          d8 += h7 * r1;
          d8 += h8 * r0;
          d8 += h9 * (5 * r9);
          c += d8 >>> 13;
          d8 &= 8191;
          d9 = c;
          d9 += h0 * r9;
          d9 += h1 * r8;
          d9 += h2 * r7;
          d9 += h3 * r6;
          d9 += h4 * r5;
          c = d9 >>> 13;
          d9 &= 8191;
          d9 += h5 * r4;
          d9 += h6 * r3;
          d9 += h7 * r2;
          d9 += h8 * r1;
          d9 += h9 * r0;
          c += d9 >>> 13;
          d9 &= 8191;
          c = (c << 2) + c | 0;
          c = c + d0 | 0;
          d0 = c & 8191;
          c = c >>> 13;
          d1 += c;
          h0 = d0;
          h1 = d1;
          h2 = d2;
          h3 = d3;
          h4 = d4;
          h5 = d5;
          h6 = d6;
          h7 = d7;
          h8 = d8;
          h9 = d9;
          mpos += 16;
          bytes -= 16;
        }
        this.h[0] = h0;
        this.h[1] = h1;
        this.h[2] = h2;
        this.h[3] = h3;
        this.h[4] = h4;
        this.h[5] = h5;
        this.h[6] = h6;
        this.h[7] = h7;
        this.h[8] = h8;
        this.h[9] = h9;
      };
      poly1305.prototype.finish = function(mac, macpos) {
        var g = new Uint16Array(10);
        var c, mask, f, i;
        if (this.leftover) {
          i = this.leftover;
          this.buffer[i++] = 1;
          for (; i < 16; i++) this.buffer[i] = 0;
          this.fin = 1;
          this.blocks(this.buffer, 0, 16);
        }
        c = this.h[1] >>> 13;
        this.h[1] &= 8191;
        for (i = 2; i < 10; i++) {
          this.h[i] += c;
          c = this.h[i] >>> 13;
          this.h[i] &= 8191;
        }
        this.h[0] += c * 5;
        c = this.h[0] >>> 13;
        this.h[0] &= 8191;
        this.h[1] += c;
        c = this.h[1] >>> 13;
        this.h[1] &= 8191;
        this.h[2] += c;
        g[0] = this.h[0] + 5;
        c = g[0] >>> 13;
        g[0] &= 8191;
        for (i = 1; i < 10; i++) {
          g[i] = this.h[i] + c;
          c = g[i] >>> 13;
          g[i] &= 8191;
        }
        g[9] -= 1 << 13;
        mask = (c ^ 1) - 1;
        for (i = 0; i < 10; i++) g[i] &= mask;
        mask = ~mask;
        for (i = 0; i < 10; i++) this.h[i] = this.h[i] & mask | g[i];
        this.h[0] = (this.h[0] | this.h[1] << 13) & 65535;
        this.h[1] = (this.h[1] >>> 3 | this.h[2] << 10) & 65535;
        this.h[2] = (this.h[2] >>> 6 | this.h[3] << 7) & 65535;
        this.h[3] = (this.h[3] >>> 9 | this.h[4] << 4) & 65535;
        this.h[4] = (this.h[4] >>> 12 | this.h[5] << 1 | this.h[6] << 14) & 65535;
        this.h[5] = (this.h[6] >>> 2 | this.h[7] << 11) & 65535;
        this.h[6] = (this.h[7] >>> 5 | this.h[8] << 8) & 65535;
        this.h[7] = (this.h[8] >>> 8 | this.h[9] << 5) & 65535;
        f = this.h[0] + this.pad[0];
        this.h[0] = f & 65535;
        for (i = 1; i < 8; i++) {
          f = (this.h[i] + this.pad[i] | 0) + (f >>> 16) | 0;
          this.h[i] = f & 65535;
        }
        mac[macpos + 0] = this.h[0] >>> 0 & 255;
        mac[macpos + 1] = this.h[0] >>> 8 & 255;
        mac[macpos + 2] = this.h[1] >>> 0 & 255;
        mac[macpos + 3] = this.h[1] >>> 8 & 255;
        mac[macpos + 4] = this.h[2] >>> 0 & 255;
        mac[macpos + 5] = this.h[2] >>> 8 & 255;
        mac[macpos + 6] = this.h[3] >>> 0 & 255;
        mac[macpos + 7] = this.h[3] >>> 8 & 255;
        mac[macpos + 8] = this.h[4] >>> 0 & 255;
        mac[macpos + 9] = this.h[4] >>> 8 & 255;
        mac[macpos + 10] = this.h[5] >>> 0 & 255;
        mac[macpos + 11] = this.h[5] >>> 8 & 255;
        mac[macpos + 12] = this.h[6] >>> 0 & 255;
        mac[macpos + 13] = this.h[6] >>> 8 & 255;
        mac[macpos + 14] = this.h[7] >>> 0 & 255;
        mac[macpos + 15] = this.h[7] >>> 8 & 255;
      };
      poly1305.prototype.update = function(m, mpos, bytes) {
        var i, want;
        if (this.leftover) {
          want = 16 - this.leftover;
          if (want > bytes)
            want = bytes;
          for (i = 0; i < want; i++)
            this.buffer[this.leftover + i] = m[mpos + i];
          bytes -= want;
          mpos += want;
          this.leftover += want;
          if (this.leftover < 16)
            return;
          this.blocks(this.buffer, 0, 16);
          this.leftover = 0;
        }
        if (bytes >= 16) {
          want = bytes - bytes % 16;
          this.blocks(m, mpos, want);
          mpos += want;
          bytes -= want;
        }
        if (bytes) {
          for (i = 0; i < bytes; i++)
            this.buffer[this.leftover + i] = m[mpos + i];
          this.leftover += bytes;
        }
      };
      function crypto_onetimeauth(out, outpos, m, mpos, n, k) {
        var s = new poly1305(k);
        s.update(m, mpos, n);
        s.finish(out, outpos);
        return 0;
      }
      function crypto_onetimeauth_verify(h, hpos, m, mpos, n, k) {
        var x = new Uint8Array(16);
        crypto_onetimeauth(x, 0, m, mpos, n, k);
        return crypto_verify_16(h, hpos, x, 0);
      }
      function crypto_secretbox(c, m, d, n, k) {
        var i;
        if (d < 32) return -1;
        crypto_stream_xor(c, 0, m, 0, d, n, k);
        crypto_onetimeauth(c, 16, c, 32, d - 32, c);
        for (i = 0; i < 16; i++) c[i] = 0;
        return 0;
      }
      function crypto_secretbox_open(m, c, d, n, k) {
        var i;
        var x = new Uint8Array(32);
        if (d < 32) return -1;
        crypto_stream(x, 0, 32, n, k);
        if (crypto_onetimeauth_verify(c, 16, c, 32, d - 32, x) !== 0) return -1;
        crypto_stream_xor(m, 0, c, 0, d, n, k);
        for (i = 0; i < 32; i++) m[i] = 0;
        return 0;
      }
      function set25519(r, a) {
        var i;
        for (i = 0; i < 16; i++) r[i] = a[i] | 0;
      }
      function car25519(o) {
        var i, v, c = 1;
        for (i = 0; i < 16; i++) {
          v = o[i] + c + 65535;
          c = Math.floor(v / 65536);
          o[i] = v - c * 65536;
        }
        o[0] += c - 1 + 37 * (c - 1);
      }
      function sel25519(p, q, b) {
        var t, c = ~(b - 1);
        for (var i = 0; i < 16; i++) {
          t = c & (p[i] ^ q[i]);
          p[i] ^= t;
          q[i] ^= t;
        }
      }
      function pack25519(o, n) {
        var i, j, b;
        var m = gf(), t = gf();
        for (i = 0; i < 16; i++) t[i] = n[i];
        car25519(t);
        car25519(t);
        car25519(t);
        for (j = 0; j < 2; j++) {
          m[0] = t[0] - 65517;
          for (i = 1; i < 15; i++) {
            m[i] = t[i] - 65535 - (m[i - 1] >> 16 & 1);
            m[i - 1] &= 65535;
          }
          m[15] = t[15] - 32767 - (m[14] >> 16 & 1);
          b = m[15] >> 16 & 1;
          m[14] &= 65535;
          sel25519(t, m, 1 - b);
        }
        for (i = 0; i < 16; i++) {
          o[2 * i] = t[i] & 255;
          o[2 * i + 1] = t[i] >> 8;
        }
      }
      function neq25519(a, b) {
        var c = new Uint8Array(32), d = new Uint8Array(32);
        pack25519(c, a);
        pack25519(d, b);
        return crypto_verify_32(c, 0, d, 0);
      }
      function par25519(a) {
        var d = new Uint8Array(32);
        pack25519(d, a);
        return d[0] & 1;
      }
      function unpack25519(o, n) {
        var i;
        for (i = 0; i < 16; i++) o[i] = n[2 * i] + (n[2 * i + 1] << 8);
        o[15] &= 32767;
      }
      function A(o, a, b) {
        for (var i = 0; i < 16; i++) o[i] = a[i] + b[i];
      }
      function Z(o, a, b) {
        for (var i = 0; i < 16; i++) o[i] = a[i] - b[i];
      }
      function M(o, a, b) {
        var v, c, t0 = 0, t1 = 0, t2 = 0, t3 = 0, t4 = 0, t5 = 0, t6 = 0, t7 = 0, t8 = 0, t9 = 0, t10 = 0, t11 = 0, t12 = 0, t13 = 0, t14 = 0, t15 = 0, t16 = 0, t17 = 0, t18 = 0, t19 = 0, t20 = 0, t21 = 0, t22 = 0, t23 = 0, t24 = 0, t25 = 0, t26 = 0, t27 = 0, t28 = 0, t29 = 0, t30 = 0, b0 = b[0], b1 = b[1], b2 = b[2], b3 = b[3], b4 = b[4], b5 = b[5], b6 = b[6], b7 = b[7], b8 = b[8], b9 = b[9], b10 = b[10], b11 = b[11], b12 = b[12], b13 = b[13], b14 = b[14], b15 = b[15];
        v = a[0];
        t0 += v * b0;
        t1 += v * b1;
        t2 += v * b2;
        t3 += v * b3;
        t4 += v * b4;
        t5 += v * b5;
        t6 += v * b6;
        t7 += v * b7;
        t8 += v * b8;
        t9 += v * b9;
        t10 += v * b10;
        t11 += v * b11;
        t12 += v * b12;
        t13 += v * b13;
        t14 += v * b14;
        t15 += v * b15;
        v = a[1];
        t1 += v * b0;
        t2 += v * b1;
        t3 += v * b2;
        t4 += v * b3;
        t5 += v * b4;
        t6 += v * b5;
        t7 += v * b6;
        t8 += v * b7;
        t9 += v * b8;
        t10 += v * b9;
        t11 += v * b10;
        t12 += v * b11;
        t13 += v * b12;
        t14 += v * b13;
        t15 += v * b14;
        t16 += v * b15;
        v = a[2];
        t2 += v * b0;
        t3 += v * b1;
        t4 += v * b2;
        t5 += v * b3;
        t6 += v * b4;
        t7 += v * b5;
        t8 += v * b6;
        t9 += v * b7;
        t10 += v * b8;
        t11 += v * b9;
        t12 += v * b10;
        t13 += v * b11;
        t14 += v * b12;
        t15 += v * b13;
        t16 += v * b14;
        t17 += v * b15;
        v = a[3];
        t3 += v * b0;
        t4 += v * b1;
        t5 += v * b2;
        t6 += v * b3;
        t7 += v * b4;
        t8 += v * b5;
        t9 += v * b6;
        t10 += v * b7;
        t11 += v * b8;
        t12 += v * b9;
        t13 += v * b10;
        t14 += v * b11;
        t15 += v * b12;
        t16 += v * b13;
        t17 += v * b14;
        t18 += v * b15;
        v = a[4];
        t4 += v * b0;
        t5 += v * b1;
        t6 += v * b2;
        t7 += v * b3;
        t8 += v * b4;
        t9 += v * b5;
        t10 += v * b6;
        t11 += v * b7;
        t12 += v * b8;
        t13 += v * b9;
        t14 += v * b10;
        t15 += v * b11;
        t16 += v * b12;
        t17 += v * b13;
        t18 += v * b14;
        t19 += v * b15;
        v = a[5];
        t5 += v * b0;
        t6 += v * b1;
        t7 += v * b2;
        t8 += v * b3;
        t9 += v * b4;
        t10 += v * b5;
        t11 += v * b6;
        t12 += v * b7;
        t13 += v * b8;
        t14 += v * b9;
        t15 += v * b10;
        t16 += v * b11;
        t17 += v * b12;
        t18 += v * b13;
        t19 += v * b14;
        t20 += v * b15;
        v = a[6];
        t6 += v * b0;
        t7 += v * b1;
        t8 += v * b2;
        t9 += v * b3;
        t10 += v * b4;
        t11 += v * b5;
        t12 += v * b6;
        t13 += v * b7;
        t14 += v * b8;
        t15 += v * b9;
        t16 += v * b10;
        t17 += v * b11;
        t18 += v * b12;
        t19 += v * b13;
        t20 += v * b14;
        t21 += v * b15;
        v = a[7];
        t7 += v * b0;
        t8 += v * b1;
        t9 += v * b2;
        t10 += v * b3;
        t11 += v * b4;
        t12 += v * b5;
        t13 += v * b6;
        t14 += v * b7;
        t15 += v * b8;
        t16 += v * b9;
        t17 += v * b10;
        t18 += v * b11;
        t19 += v * b12;
        t20 += v * b13;
        t21 += v * b14;
        t22 += v * b15;
        v = a[8];
        t8 += v * b0;
        t9 += v * b1;
        t10 += v * b2;
        t11 += v * b3;
        t12 += v * b4;
        t13 += v * b5;
        t14 += v * b6;
        t15 += v * b7;
        t16 += v * b8;
        t17 += v * b9;
        t18 += v * b10;
        t19 += v * b11;
        t20 += v * b12;
        t21 += v * b13;
        t22 += v * b14;
        t23 += v * b15;
        v = a[9];
        t9 += v * b0;
        t10 += v * b1;
        t11 += v * b2;
        t12 += v * b3;
        t13 += v * b4;
        t14 += v * b5;
        t15 += v * b6;
        t16 += v * b7;
        t17 += v * b8;
        t18 += v * b9;
        t19 += v * b10;
        t20 += v * b11;
        t21 += v * b12;
        t22 += v * b13;
        t23 += v * b14;
        t24 += v * b15;
        v = a[10];
        t10 += v * b0;
        t11 += v * b1;
        t12 += v * b2;
        t13 += v * b3;
        t14 += v * b4;
        t15 += v * b5;
        t16 += v * b6;
        t17 += v * b7;
        t18 += v * b8;
        t19 += v * b9;
        t20 += v * b10;
        t21 += v * b11;
        t22 += v * b12;
        t23 += v * b13;
        t24 += v * b14;
        t25 += v * b15;
        v = a[11];
        t11 += v * b0;
        t12 += v * b1;
        t13 += v * b2;
        t14 += v * b3;
        t15 += v * b4;
        t16 += v * b5;
        t17 += v * b6;
        t18 += v * b7;
        t19 += v * b8;
        t20 += v * b9;
        t21 += v * b10;
        t22 += v * b11;
        t23 += v * b12;
        t24 += v * b13;
        t25 += v * b14;
        t26 += v * b15;
        v = a[12];
        t12 += v * b0;
        t13 += v * b1;
        t14 += v * b2;
        t15 += v * b3;
        t16 += v * b4;
        t17 += v * b5;
        t18 += v * b6;
        t19 += v * b7;
        t20 += v * b8;
        t21 += v * b9;
        t22 += v * b10;
        t23 += v * b11;
        t24 += v * b12;
        t25 += v * b13;
        t26 += v * b14;
        t27 += v * b15;
        v = a[13];
        t13 += v * b0;
        t14 += v * b1;
        t15 += v * b2;
        t16 += v * b3;
        t17 += v * b4;
        t18 += v * b5;
        t19 += v * b6;
        t20 += v * b7;
        t21 += v * b8;
        t22 += v * b9;
        t23 += v * b10;
        t24 += v * b11;
        t25 += v * b12;
        t26 += v * b13;
        t27 += v * b14;
        t28 += v * b15;
        v = a[14];
        t14 += v * b0;
        t15 += v * b1;
        t16 += v * b2;
        t17 += v * b3;
        t18 += v * b4;
        t19 += v * b5;
        t20 += v * b6;
        t21 += v * b7;
        t22 += v * b8;
        t23 += v * b9;
        t24 += v * b10;
        t25 += v * b11;
        t26 += v * b12;
        t27 += v * b13;
        t28 += v * b14;
        t29 += v * b15;
        v = a[15];
        t15 += v * b0;
        t16 += v * b1;
        t17 += v * b2;
        t18 += v * b3;
        t19 += v * b4;
        t20 += v * b5;
        t21 += v * b6;
        t22 += v * b7;
        t23 += v * b8;
        t24 += v * b9;
        t25 += v * b10;
        t26 += v * b11;
        t27 += v * b12;
        t28 += v * b13;
        t29 += v * b14;
        t30 += v * b15;
        t0 += 38 * t16;
        t1 += 38 * t17;
        t2 += 38 * t18;
        t3 += 38 * t19;
        t4 += 38 * t20;
        t5 += 38 * t21;
        t6 += 38 * t22;
        t7 += 38 * t23;
        t8 += 38 * t24;
        t9 += 38 * t25;
        t10 += 38 * t26;
        t11 += 38 * t27;
        t12 += 38 * t28;
        t13 += 38 * t29;
        t14 += 38 * t30;
        c = 1;
        v = t0 + c + 65535;
        c = Math.floor(v / 65536);
        t0 = v - c * 65536;
        v = t1 + c + 65535;
        c = Math.floor(v / 65536);
        t1 = v - c * 65536;
        v = t2 + c + 65535;
        c = Math.floor(v / 65536);
        t2 = v - c * 65536;
        v = t3 + c + 65535;
        c = Math.floor(v / 65536);
        t3 = v - c * 65536;
        v = t4 + c + 65535;
        c = Math.floor(v / 65536);
        t4 = v - c * 65536;
        v = t5 + c + 65535;
        c = Math.floor(v / 65536);
        t5 = v - c * 65536;
        v = t6 + c + 65535;
        c = Math.floor(v / 65536);
        t6 = v - c * 65536;
        v = t7 + c + 65535;
        c = Math.floor(v / 65536);
        t7 = v - c * 65536;
        v = t8 + c + 65535;
        c = Math.floor(v / 65536);
        t8 = v - c * 65536;
        v = t9 + c + 65535;
        c = Math.floor(v / 65536);
        t9 = v - c * 65536;
        v = t10 + c + 65535;
        c = Math.floor(v / 65536);
        t10 = v - c * 65536;
        v = t11 + c + 65535;
        c = Math.floor(v / 65536);
        t11 = v - c * 65536;
        v = t12 + c + 65535;
        c = Math.floor(v / 65536);
        t12 = v - c * 65536;
        v = t13 + c + 65535;
        c = Math.floor(v / 65536);
        t13 = v - c * 65536;
        v = t14 + c + 65535;
        c = Math.floor(v / 65536);
        t14 = v - c * 65536;
        v = t15 + c + 65535;
        c = Math.floor(v / 65536);
        t15 = v - c * 65536;
        t0 += c - 1 + 37 * (c - 1);
        c = 1;
        v = t0 + c + 65535;
        c = Math.floor(v / 65536);
        t0 = v - c * 65536;
        v = t1 + c + 65535;
        c = Math.floor(v / 65536);
        t1 = v - c * 65536;
        v = t2 + c + 65535;
        c = Math.floor(v / 65536);
        t2 = v - c * 65536;
        v = t3 + c + 65535;
        c = Math.floor(v / 65536);
        t3 = v - c * 65536;
        v = t4 + c + 65535;
        c = Math.floor(v / 65536);
        t4 = v - c * 65536;
        v = t5 + c + 65535;
        c = Math.floor(v / 65536);
        t5 = v - c * 65536;
        v = t6 + c + 65535;
        c = Math.floor(v / 65536);
        t6 = v - c * 65536;
        v = t7 + c + 65535;
        c = Math.floor(v / 65536);
        t7 = v - c * 65536;
        v = t8 + c + 65535;
        c = Math.floor(v / 65536);
        t8 = v - c * 65536;
        v = t9 + c + 65535;
        c = Math.floor(v / 65536);
        t9 = v - c * 65536;
        v = t10 + c + 65535;
        c = Math.floor(v / 65536);
        t10 = v - c * 65536;
        v = t11 + c + 65535;
        c = Math.floor(v / 65536);
        t11 = v - c * 65536;
        v = t12 + c + 65535;
        c = Math.floor(v / 65536);
        t12 = v - c * 65536;
        v = t13 + c + 65535;
        c = Math.floor(v / 65536);
        t13 = v - c * 65536;
        v = t14 + c + 65535;
        c = Math.floor(v / 65536);
        t14 = v - c * 65536;
        v = t15 + c + 65535;
        c = Math.floor(v / 65536);
        t15 = v - c * 65536;
        t0 += c - 1 + 37 * (c - 1);
        o[0] = t0;
        o[1] = t1;
        o[2] = t2;
        o[3] = t3;
        o[4] = t4;
        o[5] = t5;
        o[6] = t6;
        o[7] = t7;
        o[8] = t8;
        o[9] = t9;
        o[10] = t10;
        o[11] = t11;
        o[12] = t12;
        o[13] = t13;
        o[14] = t14;
        o[15] = t15;
      }
      function S(o, a) {
        M(o, a, a);
      }
      function inv25519(o, i) {
        var c = gf();
        var a;
        for (a = 0; a < 16; a++) c[a] = i[a];
        for (a = 253; a >= 0; a--) {
          S(c, c);
          if (a !== 2 && a !== 4) M(c, c, i);
        }
        for (a = 0; a < 16; a++) o[a] = c[a];
      }
      function pow2523(o, i) {
        var c = gf();
        var a;
        for (a = 0; a < 16; a++) c[a] = i[a];
        for (a = 250; a >= 0; a--) {
          S(c, c);
          if (a !== 1) M(c, c, i);
        }
        for (a = 0; a < 16; a++) o[a] = c[a];
      }
      function crypto_scalarmult(q, n, p) {
        var z = new Uint8Array(32);
        var x = new Float64Array(80), r, i;
        var a = gf(), b = gf(), c = gf(), d = gf(), e = gf(), f = gf();
        for (i = 0; i < 31; i++) z[i] = n[i];
        z[31] = n[31] & 127 | 64;
        z[0] &= 248;
        unpack25519(x, p);
        for (i = 0; i < 16; i++) {
          b[i] = x[i];
          d[i] = a[i] = c[i] = 0;
        }
        a[0] = d[0] = 1;
        for (i = 254; i >= 0; --i) {
          r = z[i >>> 3] >>> (i & 7) & 1;
          sel25519(a, b, r);
          sel25519(c, d, r);
          A(e, a, c);
          Z(a, a, c);
          A(c, b, d);
          Z(b, b, d);
          S(d, e);
          S(f, a);
          M(a, c, a);
          M(c, b, e);
          A(e, a, c);
          Z(a, a, c);
          S(b, a);
          Z(c, d, f);
          M(a, c, _121665);
          A(a, a, d);
          M(c, c, a);
          M(a, d, f);
          M(d, b, x);
          S(b, e);
          sel25519(a, b, r);
          sel25519(c, d, r);
        }
        for (i = 0; i < 16; i++) {
          x[i + 16] = a[i];
          x[i + 32] = c[i];
          x[i + 48] = b[i];
          x[i + 64] = d[i];
        }
        var x32 = x.subarray(32);
        var x16 = x.subarray(16);
        inv25519(x32, x32);
        M(x16, x16, x32);
        pack25519(q, x16);
        return 0;
      }
      function crypto_scalarmult_base(q, n) {
        return crypto_scalarmult(q, n, _9);
      }
      function crypto_box_keypair(y, x) {
        randombytes(x, 32);
        return crypto_scalarmult_base(y, x);
      }
      function crypto_box_beforenm(k, y, x) {
        var s = new Uint8Array(32);
        crypto_scalarmult(s, x, y);
        return crypto_core_hsalsa20(k, _0, s, sigma);
      }
      var crypto_box_afternm = crypto_secretbox;
      var crypto_box_open_afternm = crypto_secretbox_open;
      function crypto_box(c, m, d, n, y, x) {
        var k = new Uint8Array(32);
        crypto_box_beforenm(k, y, x);
        return crypto_box_afternm(c, m, d, n, k);
      }
      function crypto_box_open(m, c, d, n, y, x) {
        var k = new Uint8Array(32);
        crypto_box_beforenm(k, y, x);
        return crypto_box_open_afternm(m, c, d, n, k);
      }
      var K = [
        1116352408,
        3609767458,
        1899447441,
        602891725,
        3049323471,
        3964484399,
        3921009573,
        2173295548,
        961987163,
        4081628472,
        1508970993,
        3053834265,
        2453635748,
        2937671579,
        2870763221,
        3664609560,
        3624381080,
        2734883394,
        310598401,
        1164996542,
        607225278,
        1323610764,
        1426881987,
        3590304994,
        1925078388,
        4068182383,
        2162078206,
        991336113,
        2614888103,
        633803317,
        3248222580,
        3479774868,
        3835390401,
        2666613458,
        4022224774,
        944711139,
        264347078,
        2341262773,
        604807628,
        2007800933,
        770255983,
        1495990901,
        1249150122,
        1856431235,
        1555081692,
        3175218132,
        1996064986,
        2198950837,
        2554220882,
        3999719339,
        2821834349,
        766784016,
        2952996808,
        2566594879,
        3210313671,
        3203337956,
        3336571891,
        1034457026,
        3584528711,
        2466948901,
        113926993,
        3758326383,
        338241895,
        168717936,
        666307205,
        1188179964,
        773529912,
        1546045734,
        1294757372,
        1522805485,
        1396182291,
        2643833823,
        1695183700,
        2343527390,
        1986661051,
        1014477480,
        2177026350,
        1206759142,
        2456956037,
        344077627,
        2730485921,
        1290863460,
        2820302411,
        3158454273,
        3259730800,
        3505952657,
        3345764771,
        106217008,
        3516065817,
        3606008344,
        3600352804,
        1432725776,
        4094571909,
        1467031594,
        275423344,
        851169720,
        430227734,
        3100823752,
        506948616,
        1363258195,
        659060556,
        3750685593,
        883997877,
        3785050280,
        958139571,
        3318307427,
        1322822218,
        3812723403,
        1537002063,
        2003034995,
        1747873779,
        3602036899,
        1955562222,
        1575990012,
        2024104815,
        1125592928,
        2227730452,
        2716904306,
        2361852424,
        442776044,
        2428436474,
        593698344,
        2756734187,
        3733110249,
        3204031479,
        2999351573,
        3329325298,
        3815920427,
        3391569614,
        3928383900,
        3515267271,
        566280711,
        3940187606,
        3454069534,
        4118630271,
        4000239992,
        116418474,
        1914138554,
        174292421,
        2731055270,
        289380356,
        3203993006,
        460393269,
        320620315,
        685471733,
        587496836,
        852142971,
        1086792851,
        1017036298,
        365543100,
        1126000580,
        2618297676,
        1288033470,
        3409855158,
        1501505948,
        4234509866,
        1607167915,
        987167468,
        1816402316,
        1246189591
      ];
      function crypto_hashblocks_hl(hh, hl, m, n) {
        var wh = new Int32Array(16), wl = new Int32Array(16), bh0, bh1, bh2, bh3, bh4, bh5, bh6, bh7, bl0, bl1, bl2, bl3, bl4, bl5, bl6, bl7, th, tl, i, j, h, l, a, b, c, d;
        var ah0 = hh[0], ah1 = hh[1], ah2 = hh[2], ah3 = hh[3], ah4 = hh[4], ah5 = hh[5], ah6 = hh[6], ah7 = hh[7], al0 = hl[0], al1 = hl[1], al2 = hl[2], al3 = hl[3], al4 = hl[4], al5 = hl[5], al6 = hl[6], al7 = hl[7];
        var pos = 0;
        while (n >= 128) {
          for (i = 0; i < 16; i++) {
            j = 8 * i + pos;
            wh[i] = m[j + 0] << 24 | m[j + 1] << 16 | m[j + 2] << 8 | m[j + 3];
            wl[i] = m[j + 4] << 24 | m[j + 5] << 16 | m[j + 6] << 8 | m[j + 7];
          }
          for (i = 0; i < 80; i++) {
            bh0 = ah0;
            bh1 = ah1;
            bh2 = ah2;
            bh3 = ah3;
            bh4 = ah4;
            bh5 = ah5;
            bh6 = ah6;
            bh7 = ah7;
            bl0 = al0;
            bl1 = al1;
            bl2 = al2;
            bl3 = al3;
            bl4 = al4;
            bl5 = al5;
            bl6 = al6;
            bl7 = al7;
            h = ah7;
            l = al7;
            a = l & 65535;
            b = l >>> 16;
            c = h & 65535;
            d = h >>> 16;
            h = (ah4 >>> 14 | al4 << 32 - 14) ^ (ah4 >>> 18 | al4 << 32 - 18) ^ (al4 >>> 41 - 32 | ah4 << 32 - (41 - 32));
            l = (al4 >>> 14 | ah4 << 32 - 14) ^ (al4 >>> 18 | ah4 << 32 - 18) ^ (ah4 >>> 41 - 32 | al4 << 32 - (41 - 32));
            a += l & 65535;
            b += l >>> 16;
            c += h & 65535;
            d += h >>> 16;
            h = ah4 & ah5 ^ ~ah4 & ah6;
            l = al4 & al5 ^ ~al4 & al6;
            a += l & 65535;
            b += l >>> 16;
            c += h & 65535;
            d += h >>> 16;
            h = K[i * 2];
            l = K[i * 2 + 1];
            a += l & 65535;
            b += l >>> 16;
            c += h & 65535;
            d += h >>> 16;
            h = wh[i % 16];
            l = wl[i % 16];
            a += l & 65535;
            b += l >>> 16;
            c += h & 65535;
            d += h >>> 16;
            b += a >>> 16;
            c += b >>> 16;
            d += c >>> 16;
            th = c & 65535 | d << 16;
            tl = a & 65535 | b << 16;
            h = th;
            l = tl;
            a = l & 65535;
            b = l >>> 16;
            c = h & 65535;
            d = h >>> 16;
            h = (ah0 >>> 28 | al0 << 32 - 28) ^ (al0 >>> 34 - 32 | ah0 << 32 - (34 - 32)) ^ (al0 >>> 39 - 32 | ah0 << 32 - (39 - 32));
            l = (al0 >>> 28 | ah0 << 32 - 28) ^ (ah0 >>> 34 - 32 | al0 << 32 - (34 - 32)) ^ (ah0 >>> 39 - 32 | al0 << 32 - (39 - 32));
            a += l & 65535;
            b += l >>> 16;
            c += h & 65535;
            d += h >>> 16;
            h = ah0 & ah1 ^ ah0 & ah2 ^ ah1 & ah2;
            l = al0 & al1 ^ al0 & al2 ^ al1 & al2;
            a += l & 65535;
            b += l >>> 16;
            c += h & 65535;
            d += h >>> 16;
            b += a >>> 16;
            c += b >>> 16;
            d += c >>> 16;
            bh7 = c & 65535 | d << 16;
            bl7 = a & 65535 | b << 16;
            h = bh3;
            l = bl3;
            a = l & 65535;
            b = l >>> 16;
            c = h & 65535;
            d = h >>> 16;
            h = th;
            l = tl;
            a += l & 65535;
            b += l >>> 16;
            c += h & 65535;
            d += h >>> 16;
            b += a >>> 16;
            c += b >>> 16;
            d += c >>> 16;
            bh3 = c & 65535 | d << 16;
            bl3 = a & 65535 | b << 16;
            ah1 = bh0;
            ah2 = bh1;
            ah3 = bh2;
            ah4 = bh3;
            ah5 = bh4;
            ah6 = bh5;
            ah7 = bh6;
            ah0 = bh7;
            al1 = bl0;
            al2 = bl1;
            al3 = bl2;
            al4 = bl3;
            al5 = bl4;
            al6 = bl5;
            al7 = bl6;
            al0 = bl7;
            if (i % 16 === 15) {
              for (j = 0; j < 16; j++) {
                h = wh[j];
                l = wl[j];
                a = l & 65535;
                b = l >>> 16;
                c = h & 65535;
                d = h >>> 16;
                h = wh[(j + 9) % 16];
                l = wl[(j + 9) % 16];
                a += l & 65535;
                b += l >>> 16;
                c += h & 65535;
                d += h >>> 16;
                th = wh[(j + 1) % 16];
                tl = wl[(j + 1) % 16];
                h = (th >>> 1 | tl << 32 - 1) ^ (th >>> 8 | tl << 32 - 8) ^ th >>> 7;
                l = (tl >>> 1 | th << 32 - 1) ^ (tl >>> 8 | th << 32 - 8) ^ (tl >>> 7 | th << 32 - 7);
                a += l & 65535;
                b += l >>> 16;
                c += h & 65535;
                d += h >>> 16;
                th = wh[(j + 14) % 16];
                tl = wl[(j + 14) % 16];
                h = (th >>> 19 | tl << 32 - 19) ^ (tl >>> 61 - 32 | th << 32 - (61 - 32)) ^ th >>> 6;
                l = (tl >>> 19 | th << 32 - 19) ^ (th >>> 61 - 32 | tl << 32 - (61 - 32)) ^ (tl >>> 6 | th << 32 - 6);
                a += l & 65535;
                b += l >>> 16;
                c += h & 65535;
                d += h >>> 16;
                b += a >>> 16;
                c += b >>> 16;
                d += c >>> 16;
                wh[j] = c & 65535 | d << 16;
                wl[j] = a & 65535 | b << 16;
              }
            }
          }
          h = ah0;
          l = al0;
          a = l & 65535;
          b = l >>> 16;
          c = h & 65535;
          d = h >>> 16;
          h = hh[0];
          l = hl[0];
          a += l & 65535;
          b += l >>> 16;
          c += h & 65535;
          d += h >>> 16;
          b += a >>> 16;
          c += b >>> 16;
          d += c >>> 16;
          hh[0] = ah0 = c & 65535 | d << 16;
          hl[0] = al0 = a & 65535 | b << 16;
          h = ah1;
          l = al1;
          a = l & 65535;
          b = l >>> 16;
          c = h & 65535;
          d = h >>> 16;
          h = hh[1];
          l = hl[1];
          a += l & 65535;
          b += l >>> 16;
          c += h & 65535;
          d += h >>> 16;
          b += a >>> 16;
          c += b >>> 16;
          d += c >>> 16;
          hh[1] = ah1 = c & 65535 | d << 16;
          hl[1] = al1 = a & 65535 | b << 16;
          h = ah2;
          l = al2;
          a = l & 65535;
          b = l >>> 16;
          c = h & 65535;
          d = h >>> 16;
          h = hh[2];
          l = hl[2];
          a += l & 65535;
          b += l >>> 16;
          c += h & 65535;
          d += h >>> 16;
          b += a >>> 16;
          c += b >>> 16;
          d += c >>> 16;
          hh[2] = ah2 = c & 65535 | d << 16;
          hl[2] = al2 = a & 65535 | b << 16;
          h = ah3;
          l = al3;
          a = l & 65535;
          b = l >>> 16;
          c = h & 65535;
          d = h >>> 16;
          h = hh[3];
          l = hl[3];
          a += l & 65535;
          b += l >>> 16;
          c += h & 65535;
          d += h >>> 16;
          b += a >>> 16;
          c += b >>> 16;
          d += c >>> 16;
          hh[3] = ah3 = c & 65535 | d << 16;
          hl[3] = al3 = a & 65535 | b << 16;
          h = ah4;
          l = al4;
          a = l & 65535;
          b = l >>> 16;
          c = h & 65535;
          d = h >>> 16;
          h = hh[4];
          l = hl[4];
          a += l & 65535;
          b += l >>> 16;
          c += h & 65535;
          d += h >>> 16;
          b += a >>> 16;
          c += b >>> 16;
          d += c >>> 16;
          hh[4] = ah4 = c & 65535 | d << 16;
          hl[4] = al4 = a & 65535 | b << 16;
          h = ah5;
          l = al5;
          a = l & 65535;
          b = l >>> 16;
          c = h & 65535;
          d = h >>> 16;
          h = hh[5];
          l = hl[5];
          a += l & 65535;
          b += l >>> 16;
          c += h & 65535;
          d += h >>> 16;
          b += a >>> 16;
          c += b >>> 16;
          d += c >>> 16;
          hh[5] = ah5 = c & 65535 | d << 16;
          hl[5] = al5 = a & 65535 | b << 16;
          h = ah6;
          l = al6;
          a = l & 65535;
          b = l >>> 16;
          c = h & 65535;
          d = h >>> 16;
          h = hh[6];
          l = hl[6];
          a += l & 65535;
          b += l >>> 16;
          c += h & 65535;
          d += h >>> 16;
          b += a >>> 16;
          c += b >>> 16;
          d += c >>> 16;
          hh[6] = ah6 = c & 65535 | d << 16;
          hl[6] = al6 = a & 65535 | b << 16;
          h = ah7;
          l = al7;
          a = l & 65535;
          b = l >>> 16;
          c = h & 65535;
          d = h >>> 16;
          h = hh[7];
          l = hl[7];
          a += l & 65535;
          b += l >>> 16;
          c += h & 65535;
          d += h >>> 16;
          b += a >>> 16;
          c += b >>> 16;
          d += c >>> 16;
          hh[7] = ah7 = c & 65535 | d << 16;
          hl[7] = al7 = a & 65535 | b << 16;
          pos += 128;
          n -= 128;
        }
        return n;
      }
      function crypto_hash(out, m, n) {
        var hh = new Int32Array(8), hl = new Int32Array(8), x = new Uint8Array(256), i, b = n;
        hh[0] = 1779033703;
        hh[1] = 3144134277;
        hh[2] = 1013904242;
        hh[3] = 2773480762;
        hh[4] = 1359893119;
        hh[5] = 2600822924;
        hh[6] = 528734635;
        hh[7] = 1541459225;
        hl[0] = 4089235720;
        hl[1] = 2227873595;
        hl[2] = 4271175723;
        hl[3] = 1595750129;
        hl[4] = 2917565137;
        hl[5] = 725511199;
        hl[6] = 4215389547;
        hl[7] = 327033209;
        crypto_hashblocks_hl(hh, hl, m, n);
        n %= 128;
        for (i = 0; i < n; i++) x[i] = m[b - n + i];
        x[n] = 128;
        n = 256 - 128 * (n < 112 ? 1 : 0);
        x[n - 9] = 0;
        ts64(x, n - 8, b / 536870912 | 0, b << 3);
        crypto_hashblocks_hl(hh, hl, x, n);
        for (i = 0; i < 8; i++) ts64(out, 8 * i, hh[i], hl[i]);
        return 0;
      }
      function add(p, q) {
        var a = gf(), b = gf(), c = gf(), d = gf(), e = gf(), f = gf(), g = gf(), h = gf(), t = gf();
        Z(a, p[1], p[0]);
        Z(t, q[1], q[0]);
        M(a, a, t);
        A(b, p[0], p[1]);
        A(t, q[0], q[1]);
        M(b, b, t);
        M(c, p[3], q[3]);
        M(c, c, D2);
        M(d, p[2], q[2]);
        A(d, d, d);
        Z(e, b, a);
        Z(f, d, c);
        A(g, d, c);
        A(h, b, a);
        M(p[0], e, f);
        M(p[1], h, g);
        M(p[2], g, f);
        M(p[3], e, h);
      }
      function cswap(p, q, b) {
        var i;
        for (i = 0; i < 4; i++) {
          sel25519(p[i], q[i], b);
        }
      }
      function pack(r, p) {
        var tx = gf(), ty = gf(), zi = gf();
        inv25519(zi, p[2]);
        M(tx, p[0], zi);
        M(ty, p[1], zi);
        pack25519(r, ty);
        r[31] ^= par25519(tx) << 7;
      }
      function scalarmult(p, q, s) {
        var b, i;
        set25519(p[0], gf0);
        set25519(p[1], gf1);
        set25519(p[2], gf1);
        set25519(p[3], gf0);
        for (i = 255; i >= 0; --i) {
          b = s[i / 8 | 0] >> (i & 7) & 1;
          cswap(p, q, b);
          add(q, p);
          add(p, p);
          cswap(p, q, b);
        }
      }
      function scalarbase(p, s) {
        var q = [gf(), gf(), gf(), gf()];
        set25519(q[0], X);
        set25519(q[1], Y);
        set25519(q[2], gf1);
        M(q[3], X, Y);
        scalarmult(p, q, s);
      }
      function crypto_sign_keypair(pk, sk, seeded) {
        var d = new Uint8Array(64);
        var p = [gf(), gf(), gf(), gf()];
        var i;
        if (!seeded) randombytes(sk, 32);
        crypto_hash(d, sk, 32);
        d[0] &= 248;
        d[31] &= 127;
        d[31] |= 64;
        scalarbase(p, d);
        pack(pk, p);
        for (i = 0; i < 32; i++) sk[i + 32] = pk[i];
        return 0;
      }
      var L = new Float64Array([237, 211, 245, 92, 26, 99, 18, 88, 214, 156, 247, 162, 222, 249, 222, 20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 16]);
      function modL(r, x) {
        var carry, i, j, k;
        for (i = 63; i >= 32; --i) {
          carry = 0;
          for (j = i - 32, k = i - 12; j < k; ++j) {
            x[j] += carry - 16 * x[i] * L[j - (i - 32)];
            carry = Math.floor((x[j] + 128) / 256);
            x[j] -= carry * 256;
          }
          x[j] += carry;
          x[i] = 0;
        }
        carry = 0;
        for (j = 0; j < 32; j++) {
          x[j] += carry - (x[31] >> 4) * L[j];
          carry = x[j] >> 8;
          x[j] &= 255;
        }
        for (j = 0; j < 32; j++) x[j] -= carry * L[j];
        for (i = 0; i < 32; i++) {
          x[i + 1] += x[i] >> 8;
          r[i] = x[i] & 255;
        }
      }
      function reduce(r) {
        var x = new Float64Array(64), i;
        for (i = 0; i < 64; i++) x[i] = r[i];
        for (i = 0; i < 64; i++) r[i] = 0;
        modL(r, x);
      }
      function crypto_sign(sm, m, n, sk) {
        var d = new Uint8Array(64), h = new Uint8Array(64), r = new Uint8Array(64);
        var i, j, x = new Float64Array(64);
        var p = [gf(), gf(), gf(), gf()];
        crypto_hash(d, sk, 32);
        d[0] &= 248;
        d[31] &= 127;
        d[31] |= 64;
        var smlen = n + 64;
        for (i = 0; i < n; i++) sm[64 + i] = m[i];
        for (i = 0; i < 32; i++) sm[32 + i] = d[32 + i];
        crypto_hash(r, sm.subarray(32), n + 32);
        reduce(r);
        scalarbase(p, r);
        pack(sm, p);
        for (i = 32; i < 64; i++) sm[i] = sk[i];
        crypto_hash(h, sm, n + 64);
        reduce(h);
        for (i = 0; i < 64; i++) x[i] = 0;
        for (i = 0; i < 32; i++) x[i] = r[i];
        for (i = 0; i < 32; i++) {
          for (j = 0; j < 32; j++) {
            x[i + j] += h[i] * d[j];
          }
        }
        modL(sm.subarray(32), x);
        return smlen;
      }
      function unpackneg(r, p) {
        var t = gf(), chk = gf(), num = gf(), den = gf(), den2 = gf(), den4 = gf(), den6 = gf();
        set25519(r[2], gf1);
        unpack25519(r[1], p);
        S(num, r[1]);
        M(den, num, D);
        Z(num, num, r[2]);
        A(den, r[2], den);
        S(den2, den);
        S(den4, den2);
        M(den6, den4, den2);
        M(t, den6, num);
        M(t, t, den);
        pow2523(t, t);
        M(t, t, num);
        M(t, t, den);
        M(t, t, den);
        M(r[0], t, den);
        S(chk, r[0]);
        M(chk, chk, den);
        if (neq25519(chk, num)) M(r[0], r[0], I);
        S(chk, r[0]);
        M(chk, chk, den);
        if (neq25519(chk, num)) return -1;
        if (par25519(r[0]) === p[31] >> 7) Z(r[0], gf0, r[0]);
        M(r[3], r[0], r[1]);
        return 0;
      }
      function crypto_sign_open(m, sm, n, pk) {
        var i;
        var t = new Uint8Array(32), h = new Uint8Array(64);
        var p = [gf(), gf(), gf(), gf()], q = [gf(), gf(), gf(), gf()];
        if (n < 64) return -1;
        if (unpackneg(q, pk)) return -1;
        for (i = 0; i < n; i++) m[i] = sm[i];
        for (i = 0; i < 32; i++) m[i + 32] = pk[i];
        crypto_hash(h, m, n);
        reduce(h);
        scalarmult(p, q, h);
        scalarbase(q, sm.subarray(32));
        add(p, q);
        pack(t, p);
        n -= 64;
        if (crypto_verify_32(sm, 0, t, 0)) {
          for (i = 0; i < n; i++) m[i] = 0;
          return -1;
        }
        for (i = 0; i < n; i++) m[i] = sm[i + 64];
        return n;
      }
      var crypto_secretbox_KEYBYTES = 32, crypto_secretbox_NONCEBYTES = 24, crypto_secretbox_ZEROBYTES = 32, crypto_secretbox_BOXZEROBYTES = 16, crypto_scalarmult_BYTES = 32, crypto_scalarmult_SCALARBYTES = 32, crypto_box_PUBLICKEYBYTES = 32, crypto_box_SECRETKEYBYTES = 32, crypto_box_BEFORENMBYTES = 32, crypto_box_NONCEBYTES = crypto_secretbox_NONCEBYTES, crypto_box_ZEROBYTES = crypto_secretbox_ZEROBYTES, crypto_box_BOXZEROBYTES = crypto_secretbox_BOXZEROBYTES, crypto_sign_BYTES = 64, crypto_sign_PUBLICKEYBYTES = 32, crypto_sign_SECRETKEYBYTES = 64, crypto_sign_SEEDBYTES = 32, crypto_hash_BYTES = 64;
      nacl.lowlevel = {
        crypto_core_hsalsa20,
        crypto_stream_xor,
        crypto_stream,
        crypto_stream_salsa20_xor,
        crypto_stream_salsa20,
        crypto_onetimeauth,
        crypto_onetimeauth_verify,
        crypto_verify_16,
        crypto_verify_32,
        crypto_secretbox,
        crypto_secretbox_open,
        crypto_scalarmult,
        crypto_scalarmult_base,
        crypto_box_beforenm,
        crypto_box_afternm,
        crypto_box,
        crypto_box_open,
        crypto_box_keypair,
        crypto_hash,
        crypto_sign,
        crypto_sign_keypair,
        crypto_sign_open,
        crypto_secretbox_KEYBYTES,
        crypto_secretbox_NONCEBYTES,
        crypto_secretbox_ZEROBYTES,
        crypto_secretbox_BOXZEROBYTES,
        crypto_scalarmult_BYTES,
        crypto_scalarmult_SCALARBYTES,
        crypto_box_PUBLICKEYBYTES,
        crypto_box_SECRETKEYBYTES,
        crypto_box_BEFORENMBYTES,
        crypto_box_NONCEBYTES,
        crypto_box_ZEROBYTES,
        crypto_box_BOXZEROBYTES,
        crypto_sign_BYTES,
        crypto_sign_PUBLICKEYBYTES,
        crypto_sign_SECRETKEYBYTES,
        crypto_sign_SEEDBYTES,
        crypto_hash_BYTES,
        gf,
        D,
        L,
        pack25519,
        unpack25519,
        M,
        A,
        S,
        Z,
        pow2523,
        add,
        set25519,
        modL,
        scalarmult,
        scalarbase
      };
      function checkLengths(k, n) {
        if (k.length !== crypto_secretbox_KEYBYTES) throw new Error("bad key size");
        if (n.length !== crypto_secretbox_NONCEBYTES) throw new Error("bad nonce size");
      }
      function checkBoxLengths(pk, sk) {
        if (pk.length !== crypto_box_PUBLICKEYBYTES) throw new Error("bad public key size");
        if (sk.length !== crypto_box_SECRETKEYBYTES) throw new Error("bad secret key size");
      }
      function checkArrayTypes() {
        for (var i = 0; i < arguments.length; i++) {
          if (!(arguments[i] instanceof Uint8Array))
            throw new TypeError("unexpected type, use Uint8Array");
        }
      }
      function cleanup(arr) {
        for (var i = 0; i < arr.length; i++) arr[i] = 0;
      }
      nacl.randomBytes = function(n) {
        var b = new Uint8Array(n);
        randombytes(b, n);
        return b;
      };
      nacl.secretbox = function(msg, nonce, key) {
        checkArrayTypes(msg, nonce, key);
        checkLengths(key, nonce);
        var m = new Uint8Array(crypto_secretbox_ZEROBYTES + msg.length);
        var c = new Uint8Array(m.length);
        for (var i = 0; i < msg.length; i++) m[i + crypto_secretbox_ZEROBYTES] = msg[i];
        crypto_secretbox(c, m, m.length, nonce, key);
        return c.subarray(crypto_secretbox_BOXZEROBYTES);
      };
      nacl.secretbox.open = function(box, nonce, key) {
        checkArrayTypes(box, nonce, key);
        checkLengths(key, nonce);
        var c = new Uint8Array(crypto_secretbox_BOXZEROBYTES + box.length);
        var m = new Uint8Array(c.length);
        for (var i = 0; i < box.length; i++) c[i + crypto_secretbox_BOXZEROBYTES] = box[i];
        if (c.length < 32) return null;
        if (crypto_secretbox_open(m, c, c.length, nonce, key) !== 0) return null;
        return m.subarray(crypto_secretbox_ZEROBYTES);
      };
      nacl.secretbox.keyLength = crypto_secretbox_KEYBYTES;
      nacl.secretbox.nonceLength = crypto_secretbox_NONCEBYTES;
      nacl.secretbox.overheadLength = crypto_secretbox_BOXZEROBYTES;
      nacl.scalarMult = function(n, p) {
        checkArrayTypes(n, p);
        if (n.length !== crypto_scalarmult_SCALARBYTES) throw new Error("bad n size");
        if (p.length !== crypto_scalarmult_BYTES) throw new Error("bad p size");
        var q = new Uint8Array(crypto_scalarmult_BYTES);
        crypto_scalarmult(q, n, p);
        return q;
      };
      nacl.scalarMult.base = function(n) {
        checkArrayTypes(n);
        if (n.length !== crypto_scalarmult_SCALARBYTES) throw new Error("bad n size");
        var q = new Uint8Array(crypto_scalarmult_BYTES);
        crypto_scalarmult_base(q, n);
        return q;
      };
      nacl.scalarMult.scalarLength = crypto_scalarmult_SCALARBYTES;
      nacl.scalarMult.groupElementLength = crypto_scalarmult_BYTES;
      nacl.box = function(msg, nonce, publicKey, secretKey) {
        var k = nacl.box.before(publicKey, secretKey);
        return nacl.secretbox(msg, nonce, k);
      };
      nacl.box.before = function(publicKey, secretKey) {
        checkArrayTypes(publicKey, secretKey);
        checkBoxLengths(publicKey, secretKey);
        var k = new Uint8Array(crypto_box_BEFORENMBYTES);
        crypto_box_beforenm(k, publicKey, secretKey);
        return k;
      };
      nacl.box.after = nacl.secretbox;
      nacl.box.open = function(msg, nonce, publicKey, secretKey) {
        var k = nacl.box.before(publicKey, secretKey);
        return nacl.secretbox.open(msg, nonce, k);
      };
      nacl.box.open.after = nacl.secretbox.open;
      nacl.box.keyPair = function() {
        var pk = new Uint8Array(crypto_box_PUBLICKEYBYTES);
        var sk = new Uint8Array(crypto_box_SECRETKEYBYTES);
        crypto_box_keypair(pk, sk);
        return { publicKey: pk, secretKey: sk };
      };
      nacl.box.keyPair.fromSecretKey = function(secretKey) {
        checkArrayTypes(secretKey);
        if (secretKey.length !== crypto_box_SECRETKEYBYTES)
          throw new Error("bad secret key size");
        var pk = new Uint8Array(crypto_box_PUBLICKEYBYTES);
        crypto_scalarmult_base(pk, secretKey);
        return { publicKey: pk, secretKey: new Uint8Array(secretKey) };
      };
      nacl.box.publicKeyLength = crypto_box_PUBLICKEYBYTES;
      nacl.box.secretKeyLength = crypto_box_SECRETKEYBYTES;
      nacl.box.sharedKeyLength = crypto_box_BEFORENMBYTES;
      nacl.box.nonceLength = crypto_box_NONCEBYTES;
      nacl.box.overheadLength = nacl.secretbox.overheadLength;
      nacl.sign = function(msg, secretKey) {
        checkArrayTypes(msg, secretKey);
        if (secretKey.length !== crypto_sign_SECRETKEYBYTES)
          throw new Error("bad secret key size");
        var signedMsg = new Uint8Array(crypto_sign_BYTES + msg.length);
        crypto_sign(signedMsg, msg, msg.length, secretKey);
        return signedMsg;
      };
      nacl.sign.open = function(signedMsg, publicKey) {
        checkArrayTypes(signedMsg, publicKey);
        if (publicKey.length !== crypto_sign_PUBLICKEYBYTES)
          throw new Error("bad public key size");
        var tmp = new Uint8Array(signedMsg.length);
        var mlen = crypto_sign_open(tmp, signedMsg, signedMsg.length, publicKey);
        if (mlen < 0) return null;
        var m = new Uint8Array(mlen);
        for (var i = 0; i < m.length; i++) m[i] = tmp[i];
        return m;
      };
      nacl.sign.detached = function(msg, secretKey) {
        var signedMsg = nacl.sign(msg, secretKey);
        var sig = new Uint8Array(crypto_sign_BYTES);
        for (var i = 0; i < sig.length; i++) sig[i] = signedMsg[i];
        return sig;
      };
      nacl.sign.detached.verify = function(msg, sig, publicKey) {
        checkArrayTypes(msg, sig, publicKey);
        if (sig.length !== crypto_sign_BYTES)
          throw new Error("bad signature size");
        if (publicKey.length !== crypto_sign_PUBLICKEYBYTES)
          throw new Error("bad public key size");
        var sm = new Uint8Array(crypto_sign_BYTES + msg.length);
        var m = new Uint8Array(crypto_sign_BYTES + msg.length);
        var i;
        for (i = 0; i < crypto_sign_BYTES; i++) sm[i] = sig[i];
        for (i = 0; i < msg.length; i++) sm[i + crypto_sign_BYTES] = msg[i];
        return crypto_sign_open(m, sm, sm.length, publicKey) >= 0;
      };
      nacl.sign.keyPair = function() {
        var pk = new Uint8Array(crypto_sign_PUBLICKEYBYTES);
        var sk = new Uint8Array(crypto_sign_SECRETKEYBYTES);
        crypto_sign_keypair(pk, sk);
        return { publicKey: pk, secretKey: sk };
      };
      nacl.sign.keyPair.fromSecretKey = function(secretKey) {
        checkArrayTypes(secretKey);
        if (secretKey.length !== crypto_sign_SECRETKEYBYTES)
          throw new Error("bad secret key size");
        var pk = new Uint8Array(crypto_sign_PUBLICKEYBYTES);
        for (var i = 0; i < pk.length; i++) pk[i] = secretKey[32 + i];
        return { publicKey: pk, secretKey: new Uint8Array(secretKey) };
      };
      nacl.sign.keyPair.fromSeed = function(seed) {
        checkArrayTypes(seed);
        if (seed.length !== crypto_sign_SEEDBYTES)
          throw new Error("bad seed size");
        var pk = new Uint8Array(crypto_sign_PUBLICKEYBYTES);
        var sk = new Uint8Array(crypto_sign_SECRETKEYBYTES);
        for (var i = 0; i < 32; i++) sk[i] = seed[i];
        crypto_sign_keypair(pk, sk, true);
        return { publicKey: pk, secretKey: sk };
      };
      nacl.sign.publicKeyLength = crypto_sign_PUBLICKEYBYTES;
      nacl.sign.secretKeyLength = crypto_sign_SECRETKEYBYTES;
      nacl.sign.seedLength = crypto_sign_SEEDBYTES;
      nacl.sign.signatureLength = crypto_sign_BYTES;
      nacl.hash = function(msg) {
        checkArrayTypes(msg);
        var h = new Uint8Array(crypto_hash_BYTES);
        crypto_hash(h, msg, msg.length);
        return h;
      };
      nacl.hash.hashLength = crypto_hash_BYTES;
      nacl.verify = function(x, y) {
        checkArrayTypes(x, y);
        if (x.length === 0 || y.length === 0) return false;
        if (x.length !== y.length) return false;
        return vn(x, 0, y, 0, x.length) === 0 ? true : false;
      };
      nacl.setPRNG = function(fn) {
        randombytes = fn;
      };
      (function() {
        var crypto = typeof self !== "undefined" ? self.crypto || self.msCrypto : null;
        if (crypto && crypto.getRandomValues) {
          var QUOTA = 65536;
          nacl.setPRNG(function(x, n) {
            var i, v = new Uint8Array(n);
            for (i = 0; i < n; i += QUOTA) {
              crypto.getRandomValues(v.subarray(i, i + Math.min(n - i, QUOTA)));
            }
            for (i = 0; i < n; i++) x[i] = v[i];
            cleanup(v);
          });
        } else if (typeof __require !== "undefined") {
          crypto = require_crypto();
          if (crypto && crypto.randomBytes) {
            nacl.setPRNG(function(x, n) {
              var i, v = crypto.randomBytes(n);
              for (i = 0; i < n; i++) x[i] = v[i];
              cleanup(v);
            });
          }
        }
      })();
    })(typeof module !== "undefined" && module.exports ? module.exports : self.nacl = self.nacl || {});
  }
});

// node_modules/@nats-io/nkeys/lib/nacl.js
var require_nacl = __commonJS({
  "node_modules/@nats-io/nkeys/lib/nacl.js"(exports) {
    "use strict";
    var __importDefault = exports && exports.__importDefault || function(mod) {
      return mod && mod.__esModule ? mod : { "default": mod };
    };
    Object.defineProperty(exports, "__esModule", { value: true });
    var tweetnacl_1 = __importDefault(require_nacl_fast());
    exports.default = tweetnacl_1.default;
  }
});

// node_modules/@nats-io/nkeys/lib/kp.js
var require_kp = __commonJS({
  "node_modules/@nats-io/nkeys/lib/kp.js"(exports) {
    "use strict";
    var __importDefault = exports && exports.__importDefault || function(mod) {
      return mod && mod.__esModule ? mod : { "default": mod };
    };
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.KP = void 0;
    var codec_1 = require_codec();
    var nkeys_1 = require_nkeys();
    var nacl_1 = __importDefault(require_nacl());
    var KP = class {
      seed;
      constructor(seed) {
        this.seed = seed;
      }
      getRawSeed() {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        const sd = codec_1.Codec.decodeSeed(this.seed);
        return sd.buf;
      }
      getSeed() {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        return this.seed;
      }
      getPublicKey() {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        const sd = codec_1.Codec.decodeSeed(this.seed);
        const kp = nacl_1.default.sign.keyPair.fromSeed(this.getRawSeed());
        const buf = codec_1.Codec.encode(sd.prefix, kp.publicKey);
        return new TextDecoder().decode(buf);
      }
      getPrivateKey() {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        const kp = nacl_1.default.sign.keyPair.fromSeed(this.getRawSeed());
        return codec_1.Codec.encode(nkeys_1.Prefix.Private, kp.secretKey);
      }
      sign(input) {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        const kp = nacl_1.default.sign.keyPair.fromSeed(this.getRawSeed());
        return nacl_1.default.sign.detached(input, kp.secretKey);
      }
      verify(input, sig) {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        const kp = nacl_1.default.sign.keyPair.fromSeed(this.getRawSeed());
        return nacl_1.default.sign.detached.verify(input, sig, kp.publicKey);
      }
      clear() {
        if (!this.seed) {
          return;
        }
        this.seed.fill(0);
        this.seed = void 0;
      }
      seal(_, _recipient, _nonce) {
        throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidNKeyOperation);
      }
      open(_, _sender) {
        throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidNKeyOperation);
      }
    };
    exports.KP = KP;
  }
});

// node_modules/@nats-io/nkeys/lib/public.js
var require_public = __commonJS({
  "node_modules/@nats-io/nkeys/lib/public.js"(exports) {
    "use strict";
    var __importDefault = exports && exports.__importDefault || function(mod) {
      return mod && mod.__esModule ? mod : { "default": mod };
    };
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.PublicKey = void 0;
    var codec_1 = require_codec();
    var nkeys_1 = require_nkeys();
    var nacl_1 = __importDefault(require_nacl());
    var PublicKey = class {
      publicKey;
      constructor(publicKey) {
        this.publicKey = publicKey;
      }
      getPublicKey() {
        if (!this.publicKey) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        return new TextDecoder().decode(this.publicKey);
      }
      getPrivateKey() {
        if (!this.publicKey) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.PublicKeyOnly);
      }
      getSeed() {
        if (!this.publicKey) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.PublicKeyOnly);
      }
      sign(_) {
        if (!this.publicKey) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.CannotSign);
      }
      verify(input, sig) {
        if (!this.publicKey) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        const buf = codec_1.Codec._decode(this.publicKey);
        return nacl_1.default.sign.detached.verify(input, sig, buf.slice(1));
      }
      clear() {
        if (!this.publicKey) {
          return;
        }
        this.publicKey.fill(0);
        this.publicKey = void 0;
      }
      seal(_, _recipient, _nonce) {
        throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidNKeyOperation);
      }
      open(_, _sender) {
        throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidNKeyOperation);
      }
    };
    exports.PublicKey = PublicKey;
  }
});

// node_modules/@nats-io/nkeys/lib/curve.js
var require_curve = __commonJS({
  "node_modules/@nats-io/nkeys/lib/curve.js"(exports) {
    "use strict";
    var __importDefault = exports && exports.__importDefault || function(mod) {
      return mod && mod.__esModule ? mod : { "default": mod };
    };
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.CurveKP = exports.curveNonceLen = exports.curveKeyLen = void 0;
    var nkeys_1 = require_nkeys();
    var nacl_1 = __importDefault(require_nacl());
    var codec_1 = require_codec();
    var nkeys_2 = require_nkeys();
    var base32_1 = require_base32();
    var crc16_1 = require_crc16();
    exports.curveKeyLen = 32;
    var curveDecodeLen = 35;
    exports.curveNonceLen = 24;
    var XKeyVersionV1 = [120, 107, 118, 49];
    var CurveKP = class {
      seed;
      constructor(seed) {
        this.seed = seed;
      }
      clear() {
        if (!this.seed) {
          return;
        }
        this.seed.fill(0);
        this.seed = void 0;
      }
      getPrivateKey() {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        return codec_1.Codec.encode(nkeys_2.Prefix.Private, this.seed);
      }
      getPublicKey() {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        const pub = nacl_1.default.scalarMult.base(this.seed);
        const buf = codec_1.Codec.encode(nkeys_2.Prefix.Curve, pub);
        return new TextDecoder().decode(buf);
      }
      getSeed() {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        return codec_1.Codec.encodeSeed(nkeys_2.Prefix.Curve, this.seed);
      }
      sign() {
        throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidCurveOperation);
      }
      verify() {
        throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidCurveOperation);
      }
      decodePubCurveKey(src) {
        try {
          const raw = base32_1.base32.decode(new TextEncoder().encode(src));
          if (raw.byteLength !== curveDecodeLen) {
            throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidCurveKey);
          }
          if (raw[0] !== nkeys_2.Prefix.Curve) {
            throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidPublicKey);
          }
          const checkOffset = raw.byteLength - 2;
          const dv = new DataView(raw.buffer);
          const checksum = dv.getUint16(checkOffset, true);
          const payload = raw.slice(0, checkOffset);
          if (!crc16_1.crc16.validate(payload, checksum)) {
            throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidChecksum);
          }
          return payload.slice(1);
        } catch (ex) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidRecipient, { cause: ex });
        }
      }
      seal(message, recipient, nonce) {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        if (!nonce) {
          nonce = nacl_1.default.randomBytes(exports.curveNonceLen);
        }
        const pub = this.decodePubCurveKey(recipient);
        const out = new Uint8Array(XKeyVersionV1.length + exports.curveNonceLen);
        out.set(XKeyVersionV1, 0);
        out.set(nonce, XKeyVersionV1.length);
        const encrypted = nacl_1.default.box(message, nonce, pub, this.seed);
        const fullMessage = new Uint8Array(out.length + encrypted.length);
        fullMessage.set(out);
        fullMessage.set(encrypted, out.length);
        return fullMessage;
      }
      open(message, sender) {
        if (!this.seed) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.ClearedPair);
        }
        if (message.length <= exports.curveNonceLen + XKeyVersionV1.length) {
          throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidEncrypted);
        }
        for (let i = 0; i < XKeyVersionV1.length; i++) {
          if (message[i] !== XKeyVersionV1[i]) {
            throw new nkeys_1.NKeysError(nkeys_1.NKeysErrorCode.InvalidEncrypted);
          }
        }
        const pub = this.decodePubCurveKey(sender);
        message = message.slice(XKeyVersionV1.length);
        const nonce = message.slice(0, exports.curveNonceLen);
        message = message.slice(exports.curveNonceLen);
        return nacl_1.default.box.open(message, nonce, pub, this.seed);
      }
    };
    exports.CurveKP = CurveKP;
  }
});

// node_modules/@nats-io/nkeys/lib/nkeys.js
var require_nkeys = __commonJS({
  "node_modules/@nats-io/nkeys/lib/nkeys.js"(exports) {
    "use strict";
    var __importDefault = exports && exports.__importDefault || function(mod) {
      return mod && mod.__esModule ? mod : { "default": mod };
    };
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.NKeysError = exports.NKeysErrorCode = exports.Prefixes = exports.Prefix = void 0;
    exports.createPair = createPair;
    exports.createOperator = createOperator;
    exports.createAccount = createAccount;
    exports.createUser = createUser;
    exports.createCluster = createCluster;
    exports.createServer = createServer;
    exports.createCurve = createCurve;
    exports.fromPublic = fromPublic;
    exports.fromCurveSeed = fromCurveSeed;
    exports.fromSeed = fromSeed;
    var kp_1 = require_kp();
    var public_1 = require_public();
    var codec_1 = require_codec();
    var curve_1 = require_curve();
    var nacl_1 = __importDefault(require_nacl());
    function createPair(prefix) {
      const len = prefix === Prefix.Curve ? curve_1.curveKeyLen : 32;
      const rawSeed = nacl_1.default.randomBytes(len);
      const str = codec_1.Codec.encodeSeed(prefix, new Uint8Array(rawSeed));
      return prefix === Prefix.Curve ? new curve_1.CurveKP(new Uint8Array(rawSeed)) : new kp_1.KP(str);
    }
    function createOperator() {
      return createPair(Prefix.Operator);
    }
    function createAccount() {
      return createPair(Prefix.Account);
    }
    function createUser() {
      return createPair(Prefix.User);
    }
    function createCluster() {
      return createPair(Prefix.Cluster);
    }
    function createServer() {
      return createPair(Prefix.Server);
    }
    function createCurve() {
      return createPair(Prefix.Curve);
    }
    function fromPublic(src) {
      const ba = new TextEncoder().encode(src);
      const raw = codec_1.Codec._decode(ba);
      const prefix = Prefixes.parsePrefix(raw[0]);
      if (Prefixes.isValidPublicPrefix(prefix)) {
        return new public_1.PublicKey(ba);
      }
      throw new NKeysError(NKeysErrorCode.InvalidPublicKey);
    }
    function fromCurveSeed(src) {
      const sd = codec_1.Codec.decodeSeed(src);
      if (sd.prefix !== Prefix.Curve) {
        throw new NKeysError(NKeysErrorCode.InvalidCurveSeed);
      }
      if (sd.buf.byteLength !== curve_1.curveKeyLen) {
        throw new NKeysError(NKeysErrorCode.InvalidSeedLen);
      }
      return new curve_1.CurveKP(sd.buf);
    }
    function fromSeed(src) {
      const sd = codec_1.Codec.decodeSeed(src);
      if (sd.prefix === Prefix.Curve) {
        return fromCurveSeed(src);
      }
      return new kp_1.KP(src);
    }
    var Prefix;
    (function(Prefix2) {
      Prefix2[Prefix2["Unknown"] = -1] = "Unknown";
      Prefix2[Prefix2["Seed"] = 144] = "Seed";
      Prefix2[Prefix2["Private"] = 120] = "Private";
      Prefix2[Prefix2["Operator"] = 112] = "Operator";
      Prefix2[Prefix2["Server"] = 104] = "Server";
      Prefix2[Prefix2["Cluster"] = 16] = "Cluster";
      Prefix2[Prefix2["Account"] = 0] = "Account";
      Prefix2[Prefix2["User"] = 160] = "User";
      Prefix2[Prefix2["Curve"] = 184] = "Curve";
    })(Prefix || (exports.Prefix = Prefix = {}));
    var Prefixes = class {
      static isValidPublicPrefix(prefix) {
        return prefix == Prefix.Server || prefix == Prefix.Operator || prefix == Prefix.Cluster || prefix == Prefix.Account || prefix == Prefix.User || prefix == Prefix.Curve;
      }
      static startsWithValidPrefix(s) {
        const c = s[0];
        return c == "S" || c == "P" || c == "O" || c == "N" || c == "C" || c == "A" || c == "U" || c == "X";
      }
      static isValidPrefix(prefix) {
        const v = this.parsePrefix(prefix);
        return v !== Prefix.Unknown;
      }
      static parsePrefix(v) {
        switch (v) {
          case Prefix.Seed:
            return Prefix.Seed;
          case Prefix.Private:
            return Prefix.Private;
          case Prefix.Operator:
            return Prefix.Operator;
          case Prefix.Server:
            return Prefix.Server;
          case Prefix.Cluster:
            return Prefix.Cluster;
          case Prefix.Account:
            return Prefix.Account;
          case Prefix.User:
            return Prefix.User;
          case Prefix.Curve:
            return Prefix.Curve;
          default:
            return Prefix.Unknown;
        }
      }
    };
    exports.Prefixes = Prefixes;
    var NKeysErrorCode;
    (function(NKeysErrorCode2) {
      NKeysErrorCode2["InvalidPrefixByte"] = "nkeys: invalid prefix byte";
      NKeysErrorCode2["InvalidKey"] = "nkeys: invalid key";
      NKeysErrorCode2["InvalidPublicKey"] = "nkeys: invalid public key";
      NKeysErrorCode2["InvalidSeedLen"] = "nkeys: invalid seed length";
      NKeysErrorCode2["InvalidSeed"] = "nkeys: invalid seed";
      NKeysErrorCode2["InvalidCurveSeed"] = "nkeys: invalid curve seed";
      NKeysErrorCode2["InvalidCurveKey"] = "nkeys: not a valid curve key";
      NKeysErrorCode2["InvalidCurveOperation"] = "nkeys: curve key is not valid for sign/verify";
      NKeysErrorCode2["InvalidNKeyOperation"] = "keys: only curve key can seal/open";
      NKeysErrorCode2["InvalidEncoding"] = "nkeys: invalid encoded key";
      NKeysErrorCode2["InvalidRecipient"] = "nkeys: not a valid recipient public curve key";
      NKeysErrorCode2["InvalidEncrypted"] = "nkeys: encrypted input is not valid";
      NKeysErrorCode2["CannotSign"] = "nkeys: cannot sign, no private key available";
      NKeysErrorCode2["PublicKeyOnly"] = "nkeys: no seed or private key available";
      NKeysErrorCode2["InvalidChecksum"] = "nkeys: invalid checksum";
      NKeysErrorCode2["SerializationError"] = "nkeys: serialization error";
      NKeysErrorCode2["ApiError"] = "nkeys: api error";
      NKeysErrorCode2["ClearedPair"] = "nkeys: pair is cleared";
    })(NKeysErrorCode || (exports.NKeysErrorCode = NKeysErrorCode = {}));
    var NKeysError = class extends Error {
      code;
      constructor(code, options) {
        super(code, options);
        this.code = code;
      }
    };
    exports.NKeysError = NKeysError;
  }
});

// node_modules/@nats-io/nkeys/lib/util.js
var require_util2 = __commonJS({
  "node_modules/@nats-io/nkeys/lib/util.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.encode = encode;
    exports.decode = decode;
    exports.dump = dump;
    function encode(bytes) {
      return btoa(String.fromCharCode(...bytes));
    }
    function decode(b64str) {
      const bin = atob(b64str);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) {
        bytes[i] = bin.charCodeAt(i);
      }
      return bytes;
    }
    function dump(buf, msg) {
      if (msg) {
        console.log(msg);
      }
      const a = [];
      for (let i = 0; i < buf.byteLength; i++) {
        if (i % 8 === 0) {
          a.push("\n");
        }
        let v = buf[i].toString(16);
        if (v.length === 1) {
          v = "0" + v;
        }
        a.push(v);
      }
      console.log(a.join("  "));
    }
  }
});

// node_modules/@nats-io/nkeys/lib/version.js
var require_version = __commonJS({
  "node_modules/@nats-io/nkeys/lib/version.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.version = void 0;
    exports.version = "2.0.3";
  }
});

// node_modules/@nats-io/nkeys/lib/mod.js
var require_mod2 = __commonJS({
  "node_modules/@nats-io/nkeys/lib/mod.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.version = exports.encode = exports.decode = exports.Prefixes = exports.Prefix = exports.NKeysErrorCode = exports.NKeysError = exports.fromSeed = exports.fromPublic = exports.fromCurveSeed = exports.createUser = exports.createServer = exports.createPair = exports.createOperator = exports.createCurve = exports.createCluster = exports.createAccount = void 0;
    var nkeys_1 = require_nkeys();
    Object.defineProperty(exports, "createAccount", { enumerable: true, get: function() {
      return nkeys_1.createAccount;
    } });
    Object.defineProperty(exports, "createCluster", { enumerable: true, get: function() {
      return nkeys_1.createCluster;
    } });
    Object.defineProperty(exports, "createCurve", { enumerable: true, get: function() {
      return nkeys_1.createCurve;
    } });
    Object.defineProperty(exports, "createOperator", { enumerable: true, get: function() {
      return nkeys_1.createOperator;
    } });
    Object.defineProperty(exports, "createPair", { enumerable: true, get: function() {
      return nkeys_1.createPair;
    } });
    Object.defineProperty(exports, "createServer", { enumerable: true, get: function() {
      return nkeys_1.createServer;
    } });
    Object.defineProperty(exports, "createUser", { enumerable: true, get: function() {
      return nkeys_1.createUser;
    } });
    Object.defineProperty(exports, "fromCurveSeed", { enumerable: true, get: function() {
      return nkeys_1.fromCurveSeed;
    } });
    Object.defineProperty(exports, "fromPublic", { enumerable: true, get: function() {
      return nkeys_1.fromPublic;
    } });
    Object.defineProperty(exports, "fromSeed", { enumerable: true, get: function() {
      return nkeys_1.fromSeed;
    } });
    Object.defineProperty(exports, "NKeysError", { enumerable: true, get: function() {
      return nkeys_1.NKeysError;
    } });
    Object.defineProperty(exports, "NKeysErrorCode", { enumerable: true, get: function() {
      return nkeys_1.NKeysErrorCode;
    } });
    Object.defineProperty(exports, "Prefix", { enumerable: true, get: function() {
      return nkeys_1.Prefix;
    } });
    Object.defineProperty(exports, "Prefixes", { enumerable: true, get: function() {
      return nkeys_1.Prefixes;
    } });
    var util_1 = require_util2();
    Object.defineProperty(exports, "decode", { enumerable: true, get: function() {
      return util_1.decode;
    } });
    Object.defineProperty(exports, "encode", { enumerable: true, get: function() {
      return util_1.encode;
    } });
    var version_1 = require_version();
    Object.defineProperty(exports, "version", { enumerable: true, get: function() {
      return version_1.version;
    } });
  }
});

// node_modules/@nats-io/nats-core/lib/nkeys.js
var require_nkeys2 = __commonJS({
  "node_modules/@nats-io/nats-core/lib/nkeys.js"(exports) {
    "use strict";
    var __createBinding = exports && exports.__createBinding || (Object.create ? function(o, m, k, k2) {
      if (k2 === void 0) k2 = k;
      var desc = Object.getOwnPropertyDescriptor(m, k);
      if (!desc || ("get" in desc ? !m.__esModule : desc.writable || desc.configurable)) {
        desc = { enumerable: true, get: function() {
          return m[k];
        } };
      }
      Object.defineProperty(o, k2, desc);
    } : function(o, m, k, k2) {
      if (k2 === void 0) k2 = k;
      o[k2] = m[k];
    });
    var __setModuleDefault = exports && exports.__setModuleDefault || (Object.create ? function(o, v) {
      Object.defineProperty(o, "default", { enumerable: true, value: v });
    } : function(o, v) {
      o["default"] = v;
    });
    var __importStar = exports && exports.__importStar || /* @__PURE__ */ function() {
      var ownKeys = function(o) {
        ownKeys = Object.getOwnPropertyNames || function(o2) {
          var ar = [];
          for (var k in o2) if (Object.prototype.hasOwnProperty.call(o2, k)) ar[ar.length] = k;
          return ar;
        };
        return ownKeys(o);
      };
      return function(mod) {
        if (mod && mod.__esModule) return mod;
        var result = {};
        if (mod != null) {
          for (var k = ownKeys(mod), i = 0; i < k.length; i++) if (k[i] !== "default") __createBinding(result, mod, k[i]);
        }
        __setModuleDefault(result, mod);
        return result;
      };
    }();
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.nkeys = void 0;
    exports.nkeys = __importStar(require_mod2());
  }
});

// node_modules/@nats-io/nats-core/lib/authenticator.js
var require_authenticator = __commonJS({
  "node_modules/@nats-io/nats-core/lib/authenticator.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.multiAuthenticator = multiAuthenticator;
    exports.noAuthFn = noAuthFn;
    exports.usernamePasswordAuthenticator = usernamePasswordAuthenticator;
    exports.tokenAuthenticator = tokenAuthenticator2;
    exports.nkeyAuthenticator = nkeyAuthenticator;
    exports.jwtAuthenticator = jwtAuthenticator;
    exports.credsAuthenticator = credsAuthenticator;
    var nkeys_1 = require_nkeys2();
    var encoders_1 = require_encoders();
    function multiAuthenticator(authenticators) {
      return (nonce) => {
        let auth = {};
        authenticators.forEach((a) => {
          const args = a(nonce) || {};
          auth = Object.assign(auth, args);
        });
        return auth;
      };
    }
    function noAuthFn() {
      return () => {
        return;
      };
    }
    function usernamePasswordAuthenticator(user, pass) {
      return () => {
        const u = typeof user === "function" ? user() : user;
        const p = typeof pass === "function" ? pass() : pass;
        return { user: u, pass: p };
      };
    }
    function tokenAuthenticator2(token) {
      return () => {
        const auth_token = typeof token === "function" ? token() : token;
        return { auth_token };
      };
    }
    function nkeyAuthenticator(seed) {
      return (nonce) => {
        const s = typeof seed === "function" ? seed() : seed;
        const kp = s ? nkeys_1.nkeys.fromSeed(s) : void 0;
        const nkey = kp ? kp.getPublicKey() : "";
        const challenge = encoders_1.TE.encode(nonce || "");
        const sigBytes = kp !== void 0 && nonce ? kp.sign(challenge) : void 0;
        const sig = sigBytes ? nkeys_1.nkeys.encode(sigBytes) : "";
        return { nkey, sig };
      };
    }
    function jwtAuthenticator(ajwt, seed) {
      return (nonce) => {
        const jwt = typeof ajwt === "function" ? ajwt() : ajwt;
        const fn = nkeyAuthenticator(seed);
        const { nkey, sig } = fn(nonce);
        return { jwt, nkey, sig };
      };
    }
    function credsAuthenticator(creds) {
      const fn = typeof creds !== "function" ? () => creds : creds;
      const parse = () => {
        const CREDS = /\s*(?:(?:[-]{3,}[^\n]*[-]{3,}\n)(.+)(?:\n\s*[-]{3,}[^\n]*[-]{3,}\n))/ig;
        const s = encoders_1.TD.decode(fn());
        let m = CREDS.exec(s);
        if (!m) {
          throw new Error("unable to parse credentials");
        }
        const jwt = m[1].trim();
        m = CREDS.exec(s);
        if (!m) {
          throw new Error("unable to parse credentials");
        }
        const seed = encoders_1.TE.encode(m[1].trim());
        return { jwt, seed };
      };
      const jwtFn = () => {
        const { jwt } = parse();
        return jwt;
      };
      const nkeyFn = () => {
        const { seed } = parse();
        return seed;
      };
      return jwtAuthenticator(jwtFn, nkeyFn);
    }
  }
});

// node_modules/@nats-io/nats-core/lib/options.js
var require_options = __commonJS({
  "node_modules/@nats-io/nats-core/lib/options.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.DEFAULT_RECONNECT_TIME_WAIT = exports.DEFAULT_MAX_PING_OUT = exports.DEFAULT_PING_INTERVAL = exports.DEFAULT_JITTER_TLS = exports.DEFAULT_JITTER = exports.DEFAULT_MAX_RECONNECT_ATTEMPTS = void 0;
    exports.defaultOptions = defaultOptions;
    exports.hasWsProtocol = hasWsProtocol;
    exports.buildAuthenticator = buildAuthenticator;
    exports.parseOptions = parseOptions;
    exports.checkOptions = checkOptions;
    exports.checkUnsupportedOption = checkUnsupportedOption;
    var util_1 = require_util();
    var transport_1 = require_transport();
    var core_1 = require_core();
    var authenticator_1 = require_authenticator();
    var errors_1 = require_errors();
    exports.DEFAULT_MAX_RECONNECT_ATTEMPTS = 10;
    exports.DEFAULT_JITTER = 100;
    exports.DEFAULT_JITTER_TLS = 1e3;
    exports.DEFAULT_PING_INTERVAL = 2 * 60 * 1e3;
    exports.DEFAULT_MAX_PING_OUT = 2;
    exports.DEFAULT_RECONNECT_TIME_WAIT = 2 * 1e3;
    function defaultOptions() {
      return {
        maxPingOut: exports.DEFAULT_MAX_PING_OUT,
        maxReconnectAttempts: exports.DEFAULT_MAX_RECONNECT_ATTEMPTS,
        noRandomize: false,
        pedantic: false,
        pingInterval: exports.DEFAULT_PING_INTERVAL,
        reconnect: true,
        reconnectJitter: exports.DEFAULT_JITTER,
        reconnectJitterTLS: exports.DEFAULT_JITTER_TLS,
        reconnectTimeWait: exports.DEFAULT_RECONNECT_TIME_WAIT,
        tls: void 0,
        verbose: false,
        waitOnFirstConnect: false,
        ignoreAuthErrorAbort: false
      };
    }
    function hasWsProtocol(opts) {
      if (opts) {
        let { servers } = opts;
        if (typeof servers === "string") {
          servers = [servers];
        }
        if (servers) {
          for (let i = 0; i < servers.length; i++) {
            const s = servers[i].toLowerCase();
            if (s.startsWith("ws://") || s.startsWith("wss://")) {
              return true;
            }
          }
        }
      }
      return false;
    }
    function buildAuthenticator(opts) {
      const buf = [];
      if (typeof opts.authenticator === "function") {
        buf.push(opts.authenticator);
      }
      if (Array.isArray(opts.authenticator)) {
        buf.push(...opts.authenticator);
      }
      if (opts.token) {
        buf.push((0, authenticator_1.tokenAuthenticator)(opts.token));
      }
      if (opts.user) {
        buf.push((0, authenticator_1.usernamePasswordAuthenticator)(opts.user, opts.pass));
      }
      return buf.length === 0 ? (0, authenticator_1.noAuthFn)() : (0, authenticator_1.multiAuthenticator)(buf);
    }
    function parseOptions(opts) {
      const dhp = `${core_1.DEFAULT_HOST}:${(0, transport_1.defaultPort)()}`;
      opts = opts || { servers: [dhp] };
      opts.servers = opts.servers || [];
      if (typeof opts.servers === "string") {
        opts.servers = [opts.servers];
      }
      if (opts.servers.length > 0 && opts.port) {
        throw errors_1.InvalidArgumentError.format(["servers", "port"], "are mutually exclusive");
      }
      if (opts.servers.length === 0 && opts.port) {
        opts.servers = [`${core_1.DEFAULT_HOST}:${opts.port}`];
      }
      if (opts.servers && opts.servers.length === 0) {
        opts.servers = [dhp];
      }
      const options = (0, util_1.extend)(defaultOptions(), opts);
      options.authenticator = buildAuthenticator(options);
      ["reconnectDelayHandler", "authenticator"].forEach((n) => {
        if (options[n] && typeof options[n] !== "function") {
          throw TypeError(`'${n}' must be a function`);
        }
      });
      if (!options.reconnectDelayHandler) {
        options.reconnectDelayHandler = () => {
          let extra = options.tls ? options.reconnectJitterTLS : options.reconnectJitter;
          if (extra) {
            extra++;
            extra = Math.floor(Math.random() * extra);
          }
          return options.reconnectTimeWait + extra;
        };
      }
      if (options.inboxPrefix) {
        (0, core_1.createInbox)(options.inboxPrefix);
      }
      if (options.resolve === void 0) {
        options.resolve = typeof (0, transport_1.getResolveFn)() === "function";
      }
      if (options.resolve) {
        if (typeof (0, transport_1.getResolveFn)() !== "function") {
          throw errors_1.InvalidArgumentError.format("resolve", "is not supported in the current runtime");
        }
      }
      return options;
    }
    function checkOptions(info, options) {
      const { proto, tls_required: tlsRequired, tls_available: tlsAvailable } = info;
      if ((proto === void 0 || proto < 1) && options.noEcho) {
        throw new errors_1.errors.ConnectionError(`server does not support 'noEcho'`);
      }
      const tls = tlsRequired || tlsAvailable || false;
      if (options.tls && !tls) {
        throw new errors_1.errors.ConnectionError(`server does not support 'tls'`);
      }
    }
    function checkUnsupportedOption(prop, v) {
      if (v) {
        throw errors_1.InvalidArgumentError.format(prop, "is not supported");
      }
    }
  }
});

// node_modules/@nats-io/nats-core/lib/protocol.js
var require_protocol = __commonJS({
  "node_modules/@nats-io/nats-core/lib/protocol.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.ProtocolHandler = exports.Subscriptions = exports.SubscriptionImpl = exports.Connect = exports.INFO = void 0;
    var encoders_1 = require_encoders();
    var transport_1 = require_transport();
    var util_1 = require_util();
    var databuffer_1 = require_databuffer();
    var servers_1 = require_servers();
    var queued_iterator_1 = require_queued_iterator();
    var muxsubscription_1 = require_muxsubscription();
    var heartbeats_1 = require_heartbeats();
    var parser_1 = require_parser();
    var msg_1 = require_msg();
    var semver_1 = require_semver();
    var options_1 = require_options();
    var errors_1 = require_errors();
    var FLUSH_THRESHOLD = 1024 * 32;
    exports.INFO = /^INFO\s+([^\r\n]+)\r\n/i;
    var PONG_CMD = (0, encoders_1.encode)("PONG\r\n");
    var PING_CMD = (0, encoders_1.encode)("PING\r\n");
    var ERR_RECONNECT_HANDLER_FAILED = "client option reconnectToServer handler failed";
    var ERR_RECONNECT_HANDLER_NOT_IN_POOL = "returned server is not in the pool";
    function isDelayedServer(x) {
      return x !== null && typeof x === "object" && "server" in x && "delay" in x;
    }
    var Connect = class {
      echo;
      no_responders;
      protocol;
      verbose;
      pedantic;
      jwt;
      nkey;
      sig;
      user;
      pass;
      auth_token;
      tls_required;
      name;
      lang;
      version;
      headers;
      constructor(transport, opts, nonce) {
        this.protocol = 1;
        this.version = transport.version;
        this.lang = transport.lang;
        this.echo = opts.noEcho ? false : void 0;
        this.verbose = opts.verbose;
        this.pedantic = opts.pedantic;
        this.tls_required = opts.tls ? true : void 0;
        this.name = opts.name;
        const creds = (opts && typeof opts.authenticator === "function" ? opts.authenticator(nonce) : {}) || {};
        (0, util_1.extend)(this, creds);
      }
    };
    exports.Connect = Connect;
    var SlowNotifier = class {
      slow;
      cb;
      notified;
      constructor(slow, cb) {
        this.slow = slow;
        this.cb = cb;
        this.notified = false;
      }
      maybeNotify(pending) {
        if (pending <= this.slow) {
          this.notified = false;
        } else {
          if (!this.notified) {
            this.cb(pending);
            this.notified = true;
          }
        }
      }
    };
    var SubscriptionImpl = class extends queued_iterator_1.QueuedIteratorImpl {
      sid;
      queue;
      draining;
      max;
      subject;
      drained;
      protocol;
      timer;
      info;
      cleanupFn;
      closed;
      requestSubject;
      slow;
      constructor(protocol, subject, opts = {}) {
        super();
        (0, util_1.extend)(this, opts);
        this.protocol = protocol;
        this.subject = subject;
        this.draining = false;
        this.noIterator = typeof opts.callback === "function";
        this.closed = (0, util_1.deferred)();
        const asyncTraces = !(protocol.options?.noAsyncTraces || false);
        if (opts.timeout) {
          this.timer = (0, util_1.timeout)(opts.timeout, asyncTraces);
          this.timer.then(() => {
            this.timer = void 0;
          }).catch((err) => {
            this.stop(err);
            if (this.noIterator) {
              this.callback(err, {});
            }
          });
        }
        if (!this.noIterator) {
          this.iterClosed.then((err) => {
            this.closed.resolve(err);
            this.unsubscribe();
          });
        }
      }
      setSlowNotificationFn(slow, fn) {
        this.slow = void 0;
        if (fn) {
          if (this.noIterator) {
            throw new Error("callbacks don't support slow notifications");
          }
          this.slow = new SlowNotifier(slow, fn);
        }
      }
      callback(err, msg) {
        this.cancelTimeout();
        err ? this.stop(err) : this.push(msg);
        if (!err && this.slow) {
          this.slow.maybeNotify(this.getPending());
        }
      }
      close(err) {
        if (!this.isClosed()) {
          this.cancelTimeout();
          const fn = () => {
            this.stop();
            if (this.cleanupFn) {
              try {
                this.cleanupFn(this, this.info);
              } catch (_err) {
              }
            }
            this.closed.resolve(err);
          };
          if (this.noIterator) {
            fn();
          } else {
            this.push(fn);
          }
        }
      }
      unsubscribe(max) {
        this.protocol.unsubscribe(this, max);
      }
      cancelTimeout() {
        if (this.timer) {
          this.timer.cancel();
          this.timer = void 0;
        }
      }
      drain() {
        if (this.protocol.isClosed()) {
          return Promise.reject(new errors_1.errors.ClosedConnectionError());
        }
        if (this.isClosed()) {
          return Promise.reject(new errors_1.errors.InvalidOperationError("subscription is already closed"));
        }
        if (!this.drained) {
          this.draining = true;
          this.protocol.unsub(this);
          this.drained = this.protocol.flush((0, util_1.deferred)()).then(() => {
            this.protocol.subscriptions.cancel(this);
          }).catch(() => {
            this.protocol.subscriptions.cancel(this);
          });
        }
        return this.drained;
      }
      async [Symbol.asyncDispose]() {
        if (this.protocol.isClosed() || this.isClosed()) {
          return;
        }
        if (this.drained) {
          await this.drained;
          return;
        }
        await this.drain();
      }
      isDraining() {
        return this.draining;
      }
      isClosed() {
        return this.done;
      }
      getSubject() {
        return this.subject;
      }
      getMax() {
        return this.max;
      }
      getID() {
        return this.sid;
      }
    };
    exports.SubscriptionImpl = SubscriptionImpl;
    var Subscriptions = class {
      mux;
      subs;
      sidCounter;
      constructor() {
        this.sidCounter = 0;
        this.mux = null;
        this.subs = /* @__PURE__ */ new Map();
      }
      size() {
        return this.subs.size;
      }
      add(s) {
        this.sidCounter++;
        s.sid = this.sidCounter;
        this.subs.set(s.sid, s);
        return s;
      }
      setMux(s) {
        this.mux = s;
        return s;
      }
      getMux() {
        return this.mux;
      }
      get(sid) {
        return this.subs.get(sid);
      }
      resub(s) {
        this.sidCounter++;
        this.subs.delete(s.sid);
        s.sid = this.sidCounter;
        this.subs.set(s.sid, s);
        return s;
      }
      all() {
        return Array.from(this.subs.values());
      }
      cancel(s) {
        if (s) {
          s.close();
          this.subs.delete(s.sid);
        }
      }
      handleError(err) {
        const subs = this.all();
        let sub;
        if (err.operation === "subscription") {
          sub = subs.find((s) => {
            return s.subject === err.subject && s.queue === err.queue;
          });
        } else if (err.operation === "publish") {
          sub = subs.find((s) => {
            return s.requestSubject === err.subject;
          });
        }
        if (sub) {
          sub.callback(err, {});
          sub.close(err);
          this.subs.delete(sub.sid);
          return sub !== this.mux;
        }
        return false;
      }
      close() {
        this.subs.forEach((sub) => {
          sub.close();
        });
      }
    };
    exports.Subscriptions = Subscriptions;
    var ProtocolHandler = class _ProtocolHandler {
      connected;
      connectedOnce;
      infoReceived;
      info;
      muxSubscriptions;
      options;
      outbound;
      pongs;
      subscriptions;
      transport;
      noMorePublishing;
      connectError;
      publisher;
      _closed;
      closed;
      listeners;
      heartbeats;
      parser;
      outMsgs;
      inMsgs;
      outBytes;
      inBytes;
      pendingLimit;
      lastError;
      abortReconnect;
      whyClosed;
      servers;
      server;
      features;
      connectPromise;
      dialDelay;
      raceTimer;
      constructor(options, publisher) {
        this._closed = false;
        this.connected = false;
        this.connectedOnce = false;
        this.infoReceived = false;
        this.noMorePublishing = false;
        this.abortReconnect = false;
        this.listeners = [];
        this.pendingLimit = FLUSH_THRESHOLD;
        this.outMsgs = 0;
        this.inMsgs = 0;
        this.outBytes = 0;
        this.inBytes = 0;
        this.options = options;
        this.publisher = publisher;
        this.subscriptions = new Subscriptions();
        this.muxSubscriptions = new muxsubscription_1.MuxSubscription();
        this.outbound = new databuffer_1.DataBuffer();
        this.pongs = [];
        this.whyClosed = "";
        this.pendingLimit = options.pendingLimit || this.pendingLimit;
        this.features = new semver_1.Features({ major: 0, minor: 0, micro: 0 });
        this.connectPromise = null;
        this.dialDelay = null;
        const servers = typeof options.servers === "string" ? [options.servers] : options.servers;
        this.servers = new servers_1.Servers({
          randomize: !options.noRandomize
        });
        this.servers.setServers(servers);
        this.closed = (0, util_1.deferred)();
        this.parser = new parser_1.Parser(this);
        this.heartbeats = new heartbeats_1.Heartbeat(this, this.options.pingInterval || options_1.DEFAULT_PING_INTERVAL, this.options.maxPingOut || options_1.DEFAULT_MAX_PING_OUT);
      }
      resetOutbound() {
        this.outbound.reset();
        const pongs = this.pongs;
        this.pongs = [];
        const err = new errors_1.errors.RequestError("connection disconnected");
        err.stack = "";
        pongs.forEach((p) => {
          p.reject(err);
        });
        this.parser = new parser_1.Parser(this);
        this.infoReceived = false;
      }
      dispatchStatus(status) {
        this.listeners.forEach((q) => {
          q.push(status);
        });
      }
      prepare() {
        if (this.transport) {
          this.transport.discard();
        }
        this.info = void 0;
        this.resetOutbound();
        const pong = (0, util_1.deferred)();
        pong.catch(() => {
        });
        this.pongs.unshift(pong);
        this.connectError = (err) => {
          pong.reject(err);
        };
        this.transport = (0, transport_1.newTransport)();
        this.transport.closed().then(async (_err) => {
          this.connected = false;
          if (!this.isClosed()) {
            await this.disconnected(this.transport.closeError || this.lastError);
            return;
          }
        });
        return pong;
      }
      disconnect() {
        this.dispatchStatus({ type: "staleConnection" });
        this.transport.disconnect();
      }
      reconnect() {
        if (this.connected) {
          this.dispatchStatus({
            type: "forceReconnect"
          });
          this.transport.disconnect();
        }
        return Promise.resolve();
      }
      async disconnected(err) {
        this.dispatchStatus({
          type: "disconnect",
          server: this.servers.getCurrentServer().toString()
        });
        if (this.options.reconnect) {
          await this.dialLoop().then(() => {
            this.dispatchStatus({
              type: "reconnect",
              server: this.servers.getCurrentServer().toString()
            });
            if (this.lastError instanceof errors_1.errors.UserAuthenticationExpiredError) {
              this.lastError = void 0;
            }
          }).catch((err2) => {
            this.close(err2).catch();
          });
        } else {
          await this.close(err).catch();
        }
      }
      async dial(srv) {
        const pong = this.prepare();
        try {
          this.raceTimer = (0, util_1.timeout)(this.options.timeout || 2e4);
          const cp = this.transport.connect(srv, this.options);
          await Promise.race([cp, this.raceTimer]);
          (async () => {
            try {
              for await (const b of this.transport) {
                this.parser.parse(b);
              }
            } catch (err) {
              console.log("reader closed", err);
            }
          })().then();
        } catch (err) {
          pong.reject(err);
        }
        try {
          await Promise.race([this.raceTimer, pong]);
          this.raceTimer?.cancel();
          this.connected = true;
          this.connectError = void 0;
          this.sendSubscriptions();
          this.connectedOnce = true;
          this.server.didConnect = true;
          this.server.reconnects = 0;
          this.flushPending();
          this.heartbeats.start();
        } catch (err) {
          this.raceTimer?.cancel();
          await this.transport.close(err);
          throw err;
        }
      }
      async _doDial(srv) {
        const { resolve } = this.options;
        const alts = await srv.resolve({
          fn: (0, transport_1.getResolveFn)(),
          debug: this.options.debug,
          randomize: !this.options.noRandomize,
          resolve
        });
        let lastErr = null;
        for (const a of alts) {
          try {
            lastErr = null;
            this.dispatchStatus({ type: "reconnecting" });
            await this.dial(a);
            return;
          } catch (err) {
            lastErr = err;
          }
        }
        throw lastErr;
      }
      dialLoop() {
        if (this.connectPromise === null) {
          this.connectPromise = this.dodialLoop();
          this.connectPromise.then(() => {
          }).catch(() => {
          }).finally(() => {
            this.connectPromise = null;
          });
        }
        return this.connectPromise;
      }
      async dodialLoop() {
        let lastError;
        while (true) {
          if (this._closed) {
            this.servers.clear();
          }
          const wait = this.options.reconnectDelayHandler ? this.options.reconnectDelayHandler() : options_1.DEFAULT_RECONNECT_TIME_WAIT;
          let maxWait = wait;
          const srv = this.selectServer();
          if (!srv || this.abortReconnect) {
            if (lastError) {
              throw lastError;
            } else if (this.lastError) {
              throw this.lastError;
            } else {
              throw new errors_1.errors.ConnectionError("connection refused");
            }
          }
          const now = Date.now();
          if (srv.lastConnect === 0 || srv.lastConnect + wait <= now) {
            let target = srv;
            let extraDelay = 0;
            if (this.options.reconnectToServer) {
              try {
                const snap = this.servers.snapshotForHandler();
                const r = this.options.reconnectToServer(snap, this.info ?? null);
                let picked;
                if (isDelayedServer(r)) {
                  picked = r.server;
                  extraDelay = Number.isFinite(r.delay) && r.delay > 0 ? Math.floor(r.delay) : 0;
                } else {
                  picked = r;
                }
                if (picked !== null) {
                  const found = this.servers.find(picked);
                  if (!found) {
                    throw new Error(ERR_RECONNECT_HANDLER_NOT_IN_POOL);
                  }
                  if (found !== srv) {
                    target = found;
                    this.servers.setCurrent(target);
                    this.server = target;
                  }
                }
              } catch (cause) {
                const c = cause instanceof Error ? cause : new Error(String(cause));
                throw new errors_1.errors.ConnectionError(`${ERR_RECONNECT_HANDLER_FAILED}: ${c.message}`, { cause: c });
              }
            }
            if (extraDelay > 0) {
              this.dialDelay = (0, util_1.delay)(extraDelay);
              await this.dialDelay;
            }
            target.lastConnect = Date.now();
            try {
              await this._doDial(target);
              break;
            } catch (err) {
              lastError = err;
              if (!this.connectedOnce) {
                if (this.options.waitOnFirstConnect) {
                  continue;
                }
                this.servers.removeCurrentServer();
              }
              target.reconnects++;
              const mra = this.options.maxReconnectAttempts || 0;
              if (mra !== -1 && target.reconnects >= mra) {
                this.servers.removeCurrentServer();
              }
            }
          } else {
            maxWait = Math.min(maxWait, srv.lastConnect + wait - now);
            this.dialDelay = (0, util_1.delay)(maxWait);
            await this.dialDelay;
          }
        }
      }
      static async connect(options, publisher) {
        const h = new _ProtocolHandler(options, publisher);
        await h.dialLoop();
        return h;
      }
      static toError(s) {
        let err = errors_1.errors.PermissionViolationError.parse(s);
        if (err) {
          return err;
        }
        err = errors_1.errors.UserAuthenticationExpiredError.parse(s);
        if (err) {
          return err;
        }
        err = errors_1.errors.AuthorizationError.parse(s);
        if (err) {
          return err;
        }
        return new errors_1.errors.ProtocolError(s);
      }
      processMsg(msg, data) {
        this.inMsgs++;
        this.inBytes += data.length;
        if (!this.subscriptions.sidCounter) {
          return;
        }
        const sub = this.subscriptions.get(msg.sid);
        if (!sub) {
          return;
        }
        sub.received += 1;
        if (sub.callback) {
          sub.callback(null, new msg_1.MsgImpl(msg, data, this));
        }
        if (sub.max !== void 0 && sub.received >= sub.max) {
          sub.unsubscribe();
        }
      }
      processError(m) {
        let s = (0, encoders_1.decode)(m);
        if (s.startsWith("'") && s.endsWith("'")) {
          s = s.slice(1, s.length - 1);
        }
        const err = _ProtocolHandler.toError(s);
        switch (err.constructor) {
          case errors_1.errors.PermissionViolationError: {
            const pe = err;
            const mux = this.subscriptions.getMux();
            const isMuxPermission = mux ? pe.subject === mux.subject : false;
            this.subscriptions.handleError(pe);
            this.muxSubscriptions.handleError(isMuxPermission, pe);
            if (isMuxPermission) {
              this.subscriptions.setMux(null);
            }
          }
        }
        this.dispatchStatus({ type: "error", error: err });
        this.handleError(err);
      }
      handleError(err) {
        if (err instanceof errors_1.errors.UserAuthenticationExpiredError || err instanceof errors_1.errors.AuthorizationError) {
          this.handleAuthError(err);
        }
        if (!(err instanceof errors_1.errors.PermissionViolationError)) {
          this.lastError = err;
        }
      }
      handleAuthError(err) {
        if ((this.lastError instanceof errors_1.errors.UserAuthenticationExpiredError || this.lastError instanceof errors_1.errors.AuthorizationError) && this.options.ignoreAuthErrorAbort === false) {
          this.abortReconnect = true;
        }
        if (this.connectError) {
          this.connectError(err);
        } else {
          this.disconnect();
        }
      }
      processPing() {
        this.transport.send(PONG_CMD);
      }
      processPong() {
        const cb = this.pongs.shift();
        if (cb) {
          cb.resolve();
        }
      }
      processInfo(m) {
        const info = JSON.parse((0, encoders_1.decode)(m));
        this.info = info;
        const updates = this.options && this.options.ignoreClusterUpdates ? void 0 : this.servers.update(info, this.transport.isEncrypted());
        if (!this.infoReceived) {
          this.features.update((0, semver_1.parseSemVer)(info.version));
          this.infoReceived = true;
          if (this.transport.isEncrypted()) {
            this.servers.updateTLSName();
          }
          const { version, lang } = this.transport;
          try {
            const c = new Connect({ version, lang }, this.options, info.nonce);
            if (info.headers) {
              c.headers = true;
              c.no_responders = true;
            }
            const cs = JSON.stringify(c);
            this.transport.send((0, encoders_1.encode)(`CONNECT ${cs}${transport_1.CR_LF}`));
            this.transport.send(PING_CMD);
          } catch (err) {
            this.close(err).catch();
          }
        }
        if (updates) {
          const { added, deleted } = updates;
          this.dispatchStatus({ type: "update", added, deleted });
        }
        const ldm = info.ldm !== void 0 ? info.ldm : false;
        if (ldm) {
          this.dispatchStatus({
            type: "ldm",
            server: this.servers.getCurrentServer().toString()
          });
        }
      }
      push(e) {
        switch (e.kind) {
          case parser_1.Kind.MSG: {
            const { msg, data } = e;
            this.processMsg(msg, data);
            break;
          }
          case parser_1.Kind.OK:
            break;
          case parser_1.Kind.ERR:
            this.processError(e.data);
            break;
          case parser_1.Kind.PING:
            this.processPing();
            break;
          case parser_1.Kind.PONG:
            this.processPong();
            break;
          case parser_1.Kind.INFO:
            this.processInfo(e.data);
            break;
        }
      }
      sendCommand(cmd, ...payloads) {
        const len = this.outbound.length();
        let buf;
        if (typeof cmd === "string") {
          buf = (0, encoders_1.encode)(cmd);
        } else {
          buf = cmd;
        }
        this.outbound.fill(buf, ...payloads);
        if (len === 0) {
          queueMicrotask(() => {
            this.flushPending();
          });
        } else if (this.outbound.size() >= this.pendingLimit) {
          this.flushPending();
        }
      }
      publish(subject, payload = encoders_1.Empty, options) {
        let data;
        if (payload instanceof Uint8Array) {
          data = payload;
        } else if (typeof payload === "string") {
          data = encoders_1.TE.encode(payload);
        } else {
          throw new TypeError("payload types can be strings or Uint8Array");
        }
        let len = data.length;
        options = options || {};
        options.reply = options.reply || "";
        let headers2 = encoders_1.Empty;
        let hlen = 0;
        if (options.headers) {
          if (this.info && !this.info.headers) {
            errors_1.InvalidArgumentError.format("headers", "are not available on this server");
          }
          const hdrs = options.headers;
          headers2 = hdrs.encode();
          hlen = headers2.length;
          len = data.length + hlen;
        }
        if (this.info && len > this.info.max_payload) {
          throw errors_1.InvalidArgumentError.format("payload", "max_payload size exceeded");
        }
        this.outBytes += len;
        this.outMsgs++;
        let proto;
        if (options.headers) {
          if (options.reply) {
            proto = `HPUB ${subject} ${options.reply} ${hlen} ${len}\r
`;
          } else {
            proto = `HPUB ${subject} ${hlen} ${len}\r
`;
          }
          this.sendCommand(proto, headers2, data, transport_1.CRLF);
        } else {
          if (options.reply) {
            proto = `PUB ${subject} ${options.reply} ${len}\r
`;
          } else {
            proto = `PUB ${subject} ${len}\r
`;
          }
          this.sendCommand(proto, data, transport_1.CRLF);
        }
      }
      request(r) {
        this.initMux();
        this.muxSubscriptions.add(r);
        return r;
      }
      subscribe(s) {
        this.subscriptions.add(s);
        this._subunsub(s);
        return s;
      }
      _sub(s) {
        if (s.queue) {
          this.sendCommand(`SUB ${s.subject} ${s.queue} ${s.sid}\r
`);
        } else {
          this.sendCommand(`SUB ${s.subject} ${s.sid}\r
`);
        }
      }
      _subunsub(s) {
        this._sub(s);
        if (s.max) {
          this.unsubscribe(s, s.max);
        }
        return s;
      }
      unsubscribe(s, max) {
        this.unsub(s, max);
        if (s.max === void 0 || s.received >= s.max) {
          this.subscriptions.cancel(s);
        }
      }
      unsub(s, max) {
        if (!s || this.isClosed()) {
          return;
        }
        if (max) {
          this.sendCommand(`UNSUB ${s.sid} ${max}\r
`);
        } else {
          this.sendCommand(`UNSUB ${s.sid}\r
`);
        }
        s.max = max;
      }
      resub(s, subject) {
        if (!s || this.isClosed()) {
          return;
        }
        this.unsub(s);
        s.subject = subject;
        this.subscriptions.resub(s);
        this._sub(s);
      }
      flush(p) {
        if (!p) {
          p = (0, util_1.deferred)();
        }
        this.pongs.push(p);
        this.outbound.fill(PING_CMD);
        this.flushPending();
        return p;
      }
      sendSubscriptions() {
        const cmds = [];
        this.subscriptions.all().forEach((s) => {
          const sub = s;
          if (sub.queue) {
            cmds.push(`SUB ${sub.subject} ${sub.queue} ${sub.sid}${transport_1.CR_LF}`);
          } else {
            cmds.push(`SUB ${sub.subject} ${sub.sid}${transport_1.CR_LF}`);
          }
        });
        if (cmds.length) {
          this.transport.send((0, encoders_1.encode)(cmds.join("")));
        }
      }
      async close(err) {
        if (this._closed) {
          return;
        }
        this.whyClosed = new Error("close trace").stack || "";
        this.heartbeats.cancel();
        if (this.connectError) {
          this.connectError(err);
          this.connectError = void 0;
        }
        this.muxSubscriptions.close();
        this.subscriptions.close();
        const proms = [];
        for (let i = 0; i < this.listeners.length; i++) {
          const qi = this.listeners[i];
          if (qi) {
            qi.push({ type: "close" });
            qi.stop();
            proms.push(qi.iterClosed);
          }
        }
        if (proms.length) {
          await Promise.all(proms);
        }
        this._closed = true;
        await this.transport.close(err);
        this.raceTimer?.cancel();
        this.dialDelay?.cancel();
        this.closed.resolve(err);
      }
      isClosed() {
        return this._closed;
      }
      async drain() {
        const subs = this.subscriptions.all();
        const promises = [];
        subs.forEach((sub) => {
          promises.push(sub.drain());
        });
        try {
          await Promise.allSettled(promises);
        } catch {
        } finally {
          this.noMorePublishing = true;
          await this.flush();
        }
        return this.close();
      }
      flushPending() {
        if (!this.infoReceived || !this.connected) {
          return;
        }
        if (this.outbound.size()) {
          const d = this.outbound.drain();
          this.transport.send(d);
        }
      }
      initMux() {
        const mux = this.subscriptions.getMux();
        if (!mux) {
          const inbox = this.muxSubscriptions.init(this.options.inboxPrefix);
          const sub = new SubscriptionImpl(this, `${inbox}*`);
          sub.callback = this.muxSubscriptions.dispatcher();
          this.subscriptions.setMux(sub);
          this.subscribe(sub);
        }
      }
      selectServer() {
        const server = this.servers.selectServer();
        if (server === void 0) {
          return void 0;
        }
        this.server = server;
        return this.server;
      }
      getServer() {
        return this.server;
      }
    };
    exports.ProtocolHandler = ProtocolHandler;
  }
});

// node_modules/@nats-io/nats-core/lib/request.js
var require_request = __commonJS({
  "node_modules/@nats-io/nats-core/lib/request.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.RequestOne = exports.RequestMany = exports.BaseRequest = void 0;
    var util_1 = require_util();
    var errors_1 = require_errors();
    var BaseRequest = class {
      token;
      received;
      ctx;
      requestSubject;
      mux;
      constructor(mux, requestSubject, asyncTraces = true) {
        this.mux = mux;
        this.requestSubject = requestSubject;
        this.received = 0;
        this.token = (0, util_1.randomToken)();
        if (asyncTraces) {
          this.ctx = new errors_1.RequestError();
        }
      }
    };
    exports.BaseRequest = BaseRequest;
    var RequestMany = class extends BaseRequest {
      callback;
      done;
      timer;
      max;
      opts;
      constructor(mux, requestSubject, opts = { maxWait: 1e3 }) {
        super(mux, requestSubject);
        this.opts = opts;
        if (typeof this.opts.callback !== "function") {
          throw new TypeError("callback must be a function");
        }
        this.callback = this.opts.callback;
        this.max = typeof opts.maxMessages === "number" && opts.maxMessages > 0 ? opts.maxMessages : -1;
        this.done = (0, util_1.deferred)();
        this.done.then(() => {
          this.callback(null, null);
        });
        this.timer = setTimeout(() => {
          this.cancel();
        }, opts.maxWait);
      }
      cancel(err) {
        if (err) {
          this.callback(err, null);
        }
        clearTimeout(this.timer);
        this.mux.cancel(this);
        this.done.resolve();
      }
      resolver(err, msg) {
        if (err) {
          if (this.ctx) {
            err.stack += `

${this.ctx.stack}`;
          }
          this.cancel(err);
        } else {
          this.callback(null, msg);
          if (this.opts.strategy === "count") {
            this.max--;
            if (this.max === 0) {
              this.cancel();
            }
          }
          if (this.opts.strategy === "stall") {
            clearTimeout(this.timer);
            this.timer = setTimeout(() => {
              this.cancel();
            }, this.opts.stall || 300);
          }
          if (this.opts.strategy === "sentinel") {
            if (msg && msg.data.length === 0) {
              this.cancel();
            }
          }
        }
      }
    };
    exports.RequestMany = RequestMany;
    var RequestOne = class extends BaseRequest {
      deferred;
      timer;
      constructor(mux, requestSubject, opts = { timeout: 1e3 }, asyncTraces = true) {
        super(mux, requestSubject, asyncTraces);
        this.deferred = (0, util_1.deferred)();
        this.timer = (0, util_1.timeout)(opts.timeout, asyncTraces);
      }
      resolver(err, msg) {
        if (this.timer) {
          this.timer.cancel();
        }
        if (err) {
          if (!(err instanceof errors_1.TimeoutError)) {
            if (this.ctx) {
              this.ctx.message = err.message;
              this.ctx.cause = err;
              err = this.ctx;
            } else {
              err = new errors_1.errors.RequestError(err.message, { cause: err });
            }
          }
          this.deferred.reject(err);
        } else {
          this.deferred.resolve(msg);
        }
        this.cancel();
      }
      cancel(err) {
        if (this.timer) {
          this.timer.cancel();
        }
        this.mux.cancel(this);
        this.deferred.reject(err ? err : new errors_1.RequestError("cancelled"));
      }
    };
    exports.RequestOne = RequestOne;
  }
});

// node_modules/@nats-io/nats-core/lib/nats.js
var require_nats = __commonJS({
  "node_modules/@nats-io/nats-core/lib/nats.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.NatsConnectionImpl = void 0;
    var util_1 = require_util();
    var protocol_1 = require_protocol();
    var encoders_1 = require_encoders();
    var headers_1 = require_headers();
    var semver_1 = require_semver();
    var options_1 = require_options();
    var queued_iterator_1 = require_queued_iterator();
    var request_1 = require_request();
    var core_1 = require_core();
    var errors_1 = require_errors();
    var whitespaceRegex = /[ \n\r\t]/;
    var NatsConnectionImpl = class _NatsConnectionImpl {
      options;
      protocol;
      draining;
      closeListeners;
      constructor(opts) {
        this.draining = false;
        this.options = (0, options_1.parseOptions)(opts);
      }
      static connect(opts = {}) {
        return new Promise((resolve, reject) => {
          const nc = new _NatsConnectionImpl(opts);
          protocol_1.ProtocolHandler.connect(nc.options, nc).then((ph) => {
            nc.protocol = ph;
            resolve(nc);
          }).catch((err) => {
            reject(err);
          });
        });
      }
      closed() {
        return this.protocol.closed;
      }
      async close() {
        await this.protocol.close();
      }
      _check(subject, sub, pub) {
        if (this.isClosed()) {
          throw new errors_1.errors.ClosedConnectionError();
        }
        if (sub && this.isDraining()) {
          throw new errors_1.errors.DrainingConnectionError();
        }
        if (pub && this.protocol.noMorePublishing) {
          throw new errors_1.errors.DrainingConnectionError();
        }
        subject = subject || "";
        if (subject.length === 0 || whitespaceRegex.test(subject)) {
          throw new errors_1.errors.InvalidSubjectError(subject);
        }
      }
      publish(subject, data, options) {
        this._check(subject, false, true);
        if (options?.reply) {
          this._check(options.reply, false, true);
        }
        if (typeof options?.traceOnly === "boolean") {
          const hdrs = options.headers || (0, headers_1.headers)();
          hdrs.set("Nats-Trace-Only", "true");
          options.headers = hdrs;
        }
        if (typeof options?.traceDestination === "string") {
          const hdrs = options.headers || (0, headers_1.headers)();
          hdrs.set("Nats-Trace-Dest", options.traceDestination);
          options.headers = hdrs;
        }
        this.protocol.publish(subject, data, options);
      }
      publishMessage(msg) {
        return this.publish(msg.subject, msg.data, {
          reply: msg.reply,
          headers: msg.headers
        });
      }
      respondMessage(msg) {
        if (msg.reply) {
          this.publish(msg.reply, msg.data, {
            reply: msg.reply,
            headers: msg.headers
          });
          return true;
        }
        return false;
      }
      subscribe(subject, opts = {}) {
        this._check(subject, true, false);
        const sub = new protocol_1.SubscriptionImpl(this.protocol, subject, opts);
        if (typeof opts.callback !== "function" && typeof opts.slow === "number") {
          sub.setSlowNotificationFn(opts.slow, (pending) => {
            this.protocol.dispatchStatus({
              type: "slowConsumer",
              sub,
              pending
            });
          });
        }
        this.protocol.subscribe(sub);
        return sub;
      }
      _resub(s, subject, max) {
        this._check(subject, true, false);
        const si = s;
        si.max = max;
        if (max) {
          si.max = max + si.received;
        }
        this.protocol.resub(si, subject);
      }
      // possibilities are:
      // stop on error or any non-100 status
      // AND:
      // - wait for timer
      // - wait for n messages or timer
      // - wait for unknown messages, done when empty or reset timer expires (with possible alt wait)
      // - wait for unknown messages, done when an empty payload is received or timer expires (with possible alt wait)
      requestMany(subject, data = encoders_1.Empty, opts = { maxWait: 1e3, maxMessages: -1 }) {
        const asyncTraces = !(this.protocol.options.noAsyncTraces || false);
        try {
          this._check(subject, true, true);
        } catch (err) {
          return Promise.reject(err);
        }
        opts.strategy = opts.strategy || "timer";
        opts.maxWait = opts.maxWait || 1e3;
        if (opts.maxWait < 1) {
          return Promise.reject(errors_1.InvalidArgumentError.format("timeout", "must be greater than 0"));
        }
        const qi = new queued_iterator_1.QueuedIteratorImpl();
        function stop(err) {
          qi.push(() => {
            qi.stop(err);
          });
        }
        function callback(err, msg) {
          if (err || msg === null) {
            stop(err === null ? void 0 : err);
          } else {
            qi.push(msg);
          }
        }
        if (opts.noMux) {
          const stack = asyncTraces ? new Error().stack : null;
          let max = typeof opts.maxMessages === "number" && opts.maxMessages > 0 ? opts.maxMessages : -1;
          const sub = this.subscribe((0, core_1.createInbox)(this.options.inboxPrefix), {
            callback: (err, msg) => {
              if (msg?.data?.length === 0 && msg?.headers?.status === "503") {
                err = new errors_1.errors.NoRespondersError(subject);
              }
              if (err) {
                if (stack) {
                  err.stack += `

${stack}`;
                }
                cancel(err);
                return;
              }
              callback(null, msg);
              if (opts.strategy === "count") {
                max--;
                if (max === 0) {
                  cancel();
                }
              }
              if (opts.strategy === "stall") {
                clearTimers();
                timer = setTimeout(() => {
                  cancel();
                }, 300);
              }
              if (opts.strategy === "sentinel") {
                if (msg && msg.data.length === 0) {
                  cancel();
                }
              }
            }
          });
          sub.requestSubject = subject;
          sub.closed.then(() => {
            stop();
          }).catch((err) => {
            qi.stop(err);
          });
          const cancel = (err) => {
            if (err) {
              qi.push(() => {
                throw err;
              });
            }
            clearTimers();
            sub.drain().then(() => {
              stop();
            }).catch((_err) => {
              stop();
            });
          };
          qi.iterClosed.then(() => {
            clearTimers();
            sub?.unsubscribe();
          }).catch((_err) => {
            clearTimers();
            sub?.unsubscribe();
          });
          const { headers: headers2, traceDestination, traceOnly } = opts;
          try {
            this.publish(subject, data, {
              reply: sub.getSubject(),
              headers: headers2,
              traceDestination,
              traceOnly
            });
          } catch (err) {
            cancel(err);
          }
          let timer = setTimeout(() => {
            cancel();
          }, opts.maxWait);
          const clearTimers = () => {
            if (timer) {
              clearTimeout(timer);
            }
          };
        } else {
          const rmo = opts;
          rmo.callback = callback;
          qi.iterClosed.then(() => {
            r.cancel();
          }).catch((err) => {
            r.cancel(err);
          });
          const r = new request_1.RequestMany(this.protocol.muxSubscriptions, subject, rmo);
          this.protocol.request(r);
          const { headers: headers2, traceDestination, traceOnly } = opts;
          try {
            this.publish(subject, data, {
              reply: `${this.protocol.muxSubscriptions.baseInbox}${r.token}`,
              headers: headers2,
              traceDestination,
              traceOnly
            });
          } catch (err) {
            r.cancel(err);
          }
        }
        return Promise.resolve(qi);
      }
      request(subject, data, opts = { timeout: 1e3, noMux: false }) {
        try {
          this._check(subject, true, true);
        } catch (err) {
          return Promise.reject(err);
        }
        const asyncTraces = !(this.protocol.options.noAsyncTraces || false);
        opts.timeout = opts.timeout || 1e3;
        if (opts.timeout < 1) {
          return Promise.reject(errors_1.InvalidArgumentError.format("timeout", `must be greater than 0`));
        }
        if (!opts.noMux && opts.reply) {
          return Promise.reject(errors_1.InvalidArgumentError.format(["reply", "noMux"], "are mutually exclusive"));
        }
        if (opts.noMux) {
          const inbox = opts.reply ? opts.reply : (0, core_1.createInbox)(this.options.inboxPrefix);
          const d = (0, util_1.deferred)();
          const errCtx = asyncTraces ? new errors_1.errors.RequestError("") : null;
          const sub = this.subscribe(inbox, {
            max: 1,
            timeout: opts.timeout,
            callback: (err, msg) => {
              if (msg && msg.data?.length === 0 && msg.headers?.code === 503) {
                err = new errors_1.errors.NoRespondersError(subject);
              }
              if (err) {
                if (!(err instanceof errors_1.TimeoutError)) {
                  if (errCtx) {
                    errCtx.message = err.message;
                    errCtx.cause = err;
                    err = errCtx;
                  } else {
                    err = new errors_1.errors.RequestError(err.message, { cause: err });
                  }
                }
                d.reject(err);
                sub.unsubscribe();
              } else {
                d.resolve(msg);
              }
            }
          });
          sub.requestSubject = subject;
          this.protocol.publish(subject, data, {
            reply: inbox,
            headers: opts.headers
          });
          return d;
        } else {
          const r = new request_1.RequestOne(this.protocol.muxSubscriptions, subject, opts, asyncTraces);
          this.protocol.request(r);
          const { headers: headers2, traceDestination, traceOnly } = opts;
          try {
            this.publish(subject, data, {
              reply: `${this.protocol.muxSubscriptions.baseInbox}${r.token}`,
              headers: headers2,
              traceDestination,
              traceOnly
            });
          } catch (err) {
            r.cancel(err);
          }
          const p = Promise.race([r.timer, r.deferred]);
          p.catch(() => {
            r.cancel();
          });
          return p;
        }
      }
      /** *
       * Flushes to the server. Promise resolves when round-trip completes.
       * @returns {Promise<void>}
       */
      flush() {
        if (this.isClosed()) {
          return Promise.reject(new errors_1.errors.ClosedConnectionError());
        }
        return this.protocol.flush();
      }
      drain() {
        if (this.isClosed()) {
          return Promise.reject(new errors_1.errors.ClosedConnectionError());
        }
        if (this.isDraining()) {
          return Promise.reject(new errors_1.errors.DrainingConnectionError());
        }
        this.draining = true;
        return this.protocol.drain();
      }
      async [Symbol.asyncDispose]() {
        if (this.isClosed()) {
          return;
        }
        if (this.isDraining()) {
          await this.closed();
          return;
        }
        await this.drain();
      }
      isClosed() {
        return this.protocol.isClosed();
      }
      isDraining() {
        return this.draining;
      }
      getServer() {
        const srv = this.protocol.getServer();
        return srv ? srv.listen : "";
      }
      setServers(servers) {
        this.protocol.servers.setServers(servers);
      }
      getServers() {
        return this.protocol.servers.snapshot();
      }
      status() {
        const iter = new queued_iterator_1.QueuedIteratorImpl();
        iter.iterClosed.then(() => {
          const idx = this.protocol.listeners.indexOf(iter);
          if (idx > -1) {
            this.protocol.listeners.splice(idx, 1);
          }
        });
        this.protocol.listeners.push(iter);
        return iter;
      }
      get info() {
        return this.protocol.isClosed() ? void 0 : this.protocol.info;
      }
      async context() {
        const r = await this.request(`$SYS.REQ.USER.INFO`);
        return r.json((key, value) => {
          if (key === "time") {
            return new Date(Date.parse(value));
          }
          return value;
        });
      }
      stats() {
        return {
          inBytes: this.protocol.inBytes,
          outBytes: this.protocol.outBytes,
          inMsgs: this.protocol.inMsgs,
          outMsgs: this.protocol.outMsgs
        };
      }
      getServerVersion() {
        const info = this.info;
        return info ? (0, semver_1.parseSemVer)(info.version) : void 0;
      }
      async rtt() {
        if (this.isClosed()) {
          throw new errors_1.errors.ClosedConnectionError();
        }
        if (!this.protocol.connected) {
          throw new errors_1.errors.RequestError("connection disconnected");
        }
        const start = Date.now();
        await this.flush();
        return Date.now() - start;
      }
      get features() {
        return this.protocol.features;
      }
      reconnect() {
        if (this.isClosed()) {
          return Promise.reject(new errors_1.errors.ClosedConnectionError());
        }
        if (this.isDraining()) {
          return Promise.reject(new errors_1.errors.DrainingConnectionError());
        }
        return this.protocol.reconnect();
      }
      // internal
      addCloseListener(listener) {
        if (this.closeListeners === void 0) {
          this.closeListeners = new CloseListeners(this.closed());
        }
        this.closeListeners.add(listener);
      }
      // internal
      removeCloseListener(listener) {
        if (this.closeListeners) {
          this.closeListeners.remove(listener);
        }
      }
    };
    exports.NatsConnectionImpl = NatsConnectionImpl;
    var CloseListeners = class {
      listeners;
      constructor(closed) {
        this.listeners = [];
        closed.then((err) => {
          this.notify(err);
        });
      }
      add(listener) {
        this.listeners.push(listener);
      }
      remove(listener) {
        this.listeners = this.listeners.filter((l) => l !== listener);
      }
      notify(err) {
        this.listeners.forEach((l) => {
          if (typeof l.connectionClosedCallback === "function") {
            try {
              l.connectionClosedCallback(err);
            } catch (_) {
            }
          }
        });
        this.listeners = [];
      }
    };
  }
});

// node_modules/@nats-io/nats-core/lib/types.js
var require_types = __commonJS({
  "node_modules/@nats-io/nats-core/lib/types.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.Empty = void 0;
    var encoders_1 = require_encoders();
    Object.defineProperty(exports, "Empty", { enumerable: true, get: function() {
      return encoders_1.Empty;
    } });
  }
});

// node_modules/@nats-io/nats-core/lib/bench.js
var require_bench = __commonJS({
  "node_modules/@nats-io/nats-core/lib/bench.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.Bench = exports.Metric = void 0;
    exports.throughput = throughput;
    exports.msgThroughput = msgThroughput;
    exports.humanizeBytes = humanizeBytes;
    var types_1 = require_types();
    var nuid_1 = require_nuid2();
    var util_1 = require_util();
    var Metric = class {
      name;
      duration;
      date;
      payload;
      msgs;
      lang;
      version;
      bytes;
      asyncRequests;
      min;
      max;
      constructor(name, duration) {
        this.name = name;
        this.duration = duration;
        this.date = Date.now();
        this.payload = 0;
        this.msgs = 0;
        this.bytes = 0;
      }
      toString() {
        const sec = this.duration / 1e3;
        const mps = Math.round(this.msgs / sec);
        const label = this.asyncRequests ? "asyncRequests" : "";
        let minmax = "";
        if (this.max) {
          minmax = `${this.min}/${this.max}`;
        }
        return `${this.name}${label ? " [asyncRequests]" : ""} ${humanizeNumber(mps)} msgs/sec - [${sec.toFixed(2)} secs] ~ ${throughput(this.bytes, sec)} ${minmax}`;
      }
      toCsv() {
        return `"${this.name}",${new Date(this.date).toISOString()},${this.lang},${this.version},${this.msgs},${this.payload},${this.bytes},${this.duration},${this.asyncRequests ? this.asyncRequests : false}
`;
      }
      static header() {
        return `Test,Date,Lang,Version,Count,MsgPayload,Bytes,Millis,Async
`;
      }
    };
    exports.Metric = Metric;
    var Bench = class {
      nc;
      callbacks;
      msgs;
      size;
      subject;
      asyncRequests;
      pub;
      sub;
      req;
      rep;
      perf;
      payload;
      constructor(nc, opts = {
        msgs: 1e5,
        size: 128,
        subject: "",
        asyncRequests: false,
        pub: false,
        sub: false,
        req: false,
        rep: false
      }) {
        this.nc = nc;
        this.callbacks = opts.callbacks || false;
        this.msgs = opts.msgs || 0;
        this.size = opts.size || 0;
        this.subject = opts.subject || nuid_1.nuid.next();
        this.asyncRequests = opts.asyncRequests || false;
        this.pub = opts.pub || false;
        this.sub = opts.sub || false;
        this.req = opts.req || false;
        this.rep = opts.rep || false;
        this.perf = new util_1.Perf();
        this.payload = this.size ? new Uint8Array(this.size) : types_1.Empty;
        if (!this.pub && !this.sub && !this.req && !this.rep) {
          throw new Error("no options selected");
        }
      }
      async run() {
        this.nc.closed().then((err) => {
          if (err) {
            throw err;
          }
        });
        if (this.callbacks) {
          await this.runCallbacks();
        } else {
          await this.runAsync();
        }
        return this.processMetrics();
      }
      processMetrics() {
        const nc = this.nc;
        const { lang, version } = nc.protocol.transport;
        if (this.pub && this.sub) {
          this.perf.measure("pubsub", "pubStart", "subStop");
        }
        if (this.req && this.rep) {
          this.perf.measure("reqrep", "reqStart", "reqStop");
        }
        const measures = this.perf.getEntries();
        const pubsub = measures.find((m) => m.name === "pubsub");
        const reqrep = measures.find((m) => m.name === "reqrep");
        const req = measures.find((m) => m.name === "req");
        const rep = measures.find((m) => m.name === "rep");
        const pub = measures.find((m) => m.name === "pub");
        const sub = measures.find((m) => m.name === "sub");
        const stats = this.nc.stats();
        const metrics = [];
        if (pubsub) {
          const { name, duration } = pubsub;
          const m = new Metric(name, duration);
          m.msgs = this.msgs * 2;
          m.bytes = stats.inBytes + stats.outBytes;
          m.lang = lang;
          m.version = version;
          m.payload = this.payload.length;
          metrics.push(m);
        }
        if (reqrep) {
          const { name, duration } = reqrep;
          const m = new Metric(name, duration);
          m.msgs = this.msgs * 2;
          m.bytes = stats.inBytes + stats.outBytes;
          m.lang = lang;
          m.version = version;
          m.payload = this.payload.length;
          metrics.push(m);
        }
        if (pub) {
          const { name, duration } = pub;
          const m = new Metric(name, duration);
          m.msgs = this.msgs;
          m.bytes = stats.outBytes;
          m.lang = lang;
          m.version = version;
          m.payload = this.payload.length;
          metrics.push(m);
        }
        if (sub) {
          const { name, duration } = sub;
          const m = new Metric(name, duration);
          m.msgs = this.msgs;
          m.bytes = stats.inBytes;
          m.lang = lang;
          m.version = version;
          m.payload = this.payload.length;
          metrics.push(m);
        }
        if (rep) {
          const { name, duration } = rep;
          const m = new Metric(name, duration);
          m.msgs = this.msgs;
          m.bytes = stats.inBytes + stats.outBytes;
          m.lang = lang;
          m.version = version;
          m.payload = this.payload.length;
          metrics.push(m);
        }
        if (req) {
          const { name, duration } = req;
          const m = new Metric(name, duration);
          m.msgs = this.msgs;
          m.bytes = stats.inBytes + stats.outBytes;
          m.lang = lang;
          m.version = version;
          m.payload = this.payload.length;
          metrics.push(m);
        }
        return metrics;
      }
      async runCallbacks() {
        const jobs = [];
        if (this.sub) {
          const d = (0, util_1.deferred)();
          jobs.push(d);
          let i = 0;
          this.nc.subscribe(this.subject, {
            max: this.msgs,
            callback: () => {
              i++;
              if (i === 1) {
                this.perf.mark("subStart");
              }
              if (i === this.msgs) {
                this.perf.mark("subStop");
                this.perf.measure("sub", "subStart", "subStop");
                d.resolve();
              }
            }
          });
        }
        if (this.rep) {
          const d = (0, util_1.deferred)();
          jobs.push(d);
          let i = 0;
          this.nc.subscribe(this.subject, {
            max: this.msgs,
            callback: (_, m) => {
              m.respond(this.payload);
              i++;
              if (i === 1) {
                this.perf.mark("repStart");
              }
              if (i === this.msgs) {
                this.perf.mark("repStop");
                this.perf.measure("rep", "repStart", "repStop");
                d.resolve();
              }
            }
          });
        }
        if (this.pub) {
          const job = (async () => {
            this.perf.mark("pubStart");
            for (let i = 0; i < this.msgs; i++) {
              this.nc.publish(this.subject, this.payload);
            }
            await this.nc.flush();
            this.perf.mark("pubStop");
            this.perf.measure("pub", "pubStart", "pubStop");
          })();
          jobs.push(job);
        }
        if (this.req) {
          const job = (async () => {
            if (this.asyncRequests) {
              this.perf.mark("reqStart");
              const a = [];
              for (let i = 0; i < this.msgs; i++) {
                a.push(this.nc.request(this.subject, this.payload, { timeout: 2e4 }));
              }
              await Promise.all(a);
              this.perf.mark("reqStop");
              this.perf.measure("req", "reqStart", "reqStop");
            } else {
              this.perf.mark("reqStart");
              for (let i = 0; i < this.msgs; i++) {
                await this.nc.request(this.subject);
              }
              this.perf.mark("reqStop");
              this.perf.measure("req", "reqStart", "reqStop");
            }
          })();
          jobs.push(job);
        }
        await Promise.all(jobs);
      }
      async runAsync() {
        const jobs = [];
        if (this.rep) {
          let first = false;
          const sub = this.nc.subscribe(this.subject, { max: this.msgs });
          const job = (async () => {
            for await (const m of sub) {
              if (!first) {
                this.perf.mark("repStart");
                first = true;
              }
              m.respond(this.payload);
            }
            await this.nc.flush();
            this.perf.mark("repStop");
            this.perf.measure("rep", "repStart", "repStop");
          })();
          jobs.push(job);
        }
        if (this.sub) {
          let first = false;
          const sub = this.nc.subscribe(this.subject, { max: this.msgs });
          const job = (async () => {
            for await (const _m of sub) {
              if (!first) {
                this.perf.mark("subStart");
                first = true;
              }
            }
            this.perf.mark("subStop");
            this.perf.measure("sub", "subStart", "subStop");
          })();
          jobs.push(job);
        }
        if (this.pub) {
          const job = (async () => {
            this.perf.mark("pubStart");
            for (let i = 0; i < this.msgs; i++) {
              this.nc.publish(this.subject, this.payload);
            }
            await this.nc.flush();
            this.perf.mark("pubStop");
            this.perf.measure("pub", "pubStart", "pubStop");
          })();
          jobs.push(job);
        }
        if (this.req) {
          const job = (async () => {
            if (this.asyncRequests) {
              this.perf.mark("reqStart");
              const a = [];
              for (let i = 0; i < this.msgs; i++) {
                a.push(this.nc.request(this.subject, this.payload, { timeout: 2e4 }));
              }
              await Promise.all(a);
              this.perf.mark("reqStop");
              this.perf.measure("req", "reqStart", "reqStop");
            } else {
              this.perf.mark("reqStart");
              for (let i = 0; i < this.msgs; i++) {
                await this.nc.request(this.subject);
              }
              this.perf.mark("reqStop");
              this.perf.measure("req", "reqStart", "reqStop");
            }
          })();
          jobs.push(job);
        }
        await Promise.all(jobs);
      }
    };
    exports.Bench = Bench;
    function throughput(bytes, seconds) {
      return `${humanizeBytes(bytes / seconds)}/sec`;
    }
    function msgThroughput(msgs, seconds) {
      return `${Math.floor(msgs / seconds)} msgs/sec`;
    }
    function humanizeBytes(bytes, si = false) {
      const base = si ? 1e3 : 1024;
      const pre = si ? ["k", "M", "G", "T", "P", "E"] : ["K", "M", "G", "T", "P", "E"];
      const post = si ? "iB" : "B";
      if (bytes < base) {
        return `${bytes.toFixed(2)} ${post}`;
      }
      const exp = parseInt(Math.log(bytes) / Math.log(base) + "");
      const index = parseInt(exp - 1 + "");
      return `${(bytes / Math.pow(base, exp)).toFixed(2)} ${pre[index]}${post}`;
    }
    function humanizeNumber(n) {
      return n.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ",");
    }
  }
});

// node_modules/@nats-io/nats-core/lib/idleheartbeat_monitor.js
var require_idleheartbeat_monitor = __commonJS({
  "node_modules/@nats-io/nats-core/lib/idleheartbeat_monitor.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.IdleHeartbeatMonitor = void 0;
    var IdleHeartbeatMonitor = class {
      interval;
      maxOut;
      cancelAfter;
      timer;
      autoCancelTimer;
      last;
      missed;
      count;
      callback;
      /**
       * Constructor
       * @param interval in millis to check
       * @param cb a callback to report when heartbeats are missed
       * @param opts monitor options @see IdleHeartbeatOptions
       */
      constructor(interval, cb, opts = { maxOut: 2 }) {
        this.interval = interval;
        this.maxOut = opts?.maxOut || 2;
        this.cancelAfter = opts?.cancelAfter || 0;
        this.last = Date.now();
        this.missed = 0;
        this.count = 0;
        this.callback = cb;
        this._schedule();
      }
      /**
       * cancel monitoring
       */
      cancel() {
        if (this.autoCancelTimer) {
          clearTimeout(this.autoCancelTimer);
        }
        if (this.timer) {
          clearInterval(this.timer);
        }
        this.timer = 0;
        this.autoCancelTimer = 0;
        this.missed = 0;
      }
      /**
       * work signals that there was work performed
       */
      work() {
        this.last = Date.now();
        this.missed = 0;
      }
      /**
       * internal api to change the interval, cancelAfter and maxOut
       * @param interval
       * @param cancelAfter
       * @param maxOut
       */
      _change(interval, cancelAfter = 0, maxOut = 2) {
        this.interval = interval;
        this.maxOut = maxOut;
        this.cancelAfter = cancelAfter;
        this.restart();
      }
      /**
       * cancels and restarts the monitoring
       */
      restart() {
        this.cancel();
        this._schedule();
      }
      /**
       * internal api called to start monitoring
       */
      _schedule() {
        if (this.cancelAfter > 0) {
          this.autoCancelTimer = setTimeout(() => {
            this.cancel();
          }, this.cancelAfter);
        }
        this.timer = setInterval(() => {
          this.count++;
          if (Date.now() - this.last > this.interval) {
            this.missed++;
          }
          if (this.missed >= this.maxOut) {
            try {
              if (this.callback(this.missed) === true) {
                this.cancel();
              }
            } catch (err) {
              console.log(err);
            }
          }
        }, this.interval);
      }
    };
    exports.IdleHeartbeatMonitor = IdleHeartbeatMonitor;
  }
});

// node_modules/@nats-io/nats-core/lib/version.js
var require_version2 = __commonJS({
  "node_modules/@nats-io/nats-core/lib/version.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.version = void 0;
    exports.version = "3.4.0";
  }
});

// node_modules/@nats-io/nats-core/lib/ws_transport.js
var require_ws_transport = __commonJS({
  "node_modules/@nats-io/nats-core/lib/ws_transport.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.WsTransport = void 0;
    exports.wsUrlParseFn = wsUrlParseFn;
    exports.wsconnect = wsconnect2;
    var util_1 = require_util();
    var transport_1 = require_transport();
    var options_1 = require_options();
    var databuffer_1 = require_databuffer();
    var protocol_1 = require_protocol();
    var nats_1 = require_nats();
    var version_1 = require_version2();
    var errors_1 = require_errors();
    var VERSION = version_1.version;
    var LANG = "nats.ws";
    var WsTransport = class {
      version;
      lang;
      closeError;
      connected;
      done;
      // @ts-ignore: expecting global WebSocket
      socket;
      options;
      socketClosed;
      encrypted;
      peeked;
      yields;
      signal;
      closedNotification;
      constructor() {
        this.version = VERSION;
        this.lang = LANG;
        this.connected = false;
        this.done = false;
        this.socketClosed = false;
        this.encrypted = false;
        this.peeked = false;
        this.yields = [];
        this.signal = (0, util_1.deferred)();
        this.closedNotification = (0, util_1.deferred)();
      }
      async connect(server, options) {
        const connected = false;
        const ok = (0, util_1.deferred)();
        this.options = options;
        const u = server.src;
        if (options.wsFactory) {
          const { socket, encrypted } = await options.wsFactory(server.src, options);
          this.socket = socket;
          this.encrypted = encrypted;
        } else {
          this.encrypted = u.indexOf("wss://") === 0;
          this.socket = new WebSocket(u);
        }
        this.socket.binaryType = "arraybuffer";
        this.socket.onopen = () => {
          if (this.done) {
            this._closed(new Error("aborted"));
          }
        };
        this.socket.onmessage = (me) => {
          if (this.done) {
            return;
          }
          this.yields.push(new Uint8Array(me.data));
          if (this.peeked) {
            this.signal.resolve();
            return;
          }
          const t = databuffer_1.DataBuffer.concat(...this.yields);
          const pm = (0, transport_1.extractProtocolMessage)(t);
          if (pm !== "") {
            const m = protocol_1.INFO.exec(pm);
            if (!m) {
              if (options.debug) {
                console.error("!!!", (0, util_1.render)(t));
              }
              ok.reject(new Error("unexpected response from server"));
              return;
            }
            try {
              const info = JSON.parse(m[1]);
              (0, options_1.checkOptions)(info, this.options);
              this.peeked = true;
              this.connected = true;
              this.signal.resolve();
              ok.resolve();
            } catch (err) {
              ok.reject(err);
              return;
            }
          }
        };
        this.socket.onclose = (evt) => {
          let reason;
          if (!evt.wasClean && evt.reason !== "") {
            reason = new Error(evt.reason);
          }
          this._closed(reason);
          this._cleanup();
        };
        this.socket.onerror = (e) => {
          if (this.done) {
            return;
          }
          const evt = e;
          const err = new errors_1.errors.ConnectionError(evt.message);
          if (!connected) {
            ok.reject(err);
          } else {
            this._closed(err);
          }
          this._cleanup();
        };
        return ok;
      }
      _cleanup() {
        if (this.socketClosed === false) {
          this.socketClosed = true;
          this.socket.onopen = null;
          this.socket.onmessage = null;
          this.socket.onerror = null;
          this.socket.onclose = null;
          this.closedNotification.resolve(this.closeError);
        }
      }
      disconnect() {
        this._closed(void 0, true);
      }
      async _closed(err, _internal = true) {
        if (this.done) {
          try {
            this.socket.close();
          } catch (_) {
          }
          return;
        }
        this.closeError = err;
        if (!err) {
          while (!this.socketClosed && this.socket.bufferedAmount > 0) {
            await (0, util_1.delay)(100);
          }
        }
        this.done = true;
        try {
          this.socket.close();
        } catch (_) {
        }
        return this.closedNotification;
      }
      get isClosed() {
        return this.done;
      }
      [Symbol.asyncIterator]() {
        return this.iterate();
      }
      async *iterate() {
        while (true) {
          if (this.done) {
            return;
          }
          if (this.yields.length === 0) {
            await this.signal;
          }
          const yields = this.yields;
          this.yields = [];
          for (let i = 0; i < yields.length; i++) {
            if (this.options.debug) {
              console.info(`> ${(0, util_1.render)(yields[i])}`);
            }
            yield yields[i];
          }
          if (this.done) {
            break;
          } else if (this.yields.length === 0) {
            yields.length = 0;
            this.yields = yields;
            this.signal = (0, util_1.deferred)();
          }
        }
      }
      isEncrypted() {
        return this.connected && this.encrypted;
      }
      send(frame) {
        if (this.done) {
          return;
        }
        try {
          this.socket.send(frame.buffer);
          if (this.options.debug) {
            console.info(`< ${(0, util_1.render)(frame)}`);
          }
          return;
        } catch (err) {
          if (this.options.debug) {
            console.error(`!!! ${(0, util_1.render)(frame)}: ${err}`);
          }
        }
      }
      close(err) {
        return this._closed(err, false);
      }
      closed() {
        return this.closedNotification;
      }
      // this is to allow a force discard on a connection
      // if the connection fails during the handshake protocol.
      // Firefox for example, will keep connections going,
      // so eventually if it succeeds, the client will have
      // an additional transport running. With this
      discard() {
        this.socket?.close();
      }
    };
    exports.WsTransport = WsTransport;
    function wsUrlParseFn(u, encrypted) {
      const ut = /^(.*:\/\/)(.*)/;
      if (!ut.test(u)) {
        if (typeof encrypted === "boolean") {
          u = `${encrypted === true ? "https" : "http"}://${u}`;
        } else {
          u = `https://${u}`;
        }
      }
      let url = new URL(u);
      const srcProto = url.protocol.toLowerCase();
      if (srcProto === "ws:") {
        encrypted = false;
      }
      if (srcProto === "wss:") {
        encrypted = true;
      }
      if (srcProto !== "https:" && srcProto !== "http") {
        u = u.replace(/^(.*:\/\/)(.*)/gm, "$2");
        url = new URL(`http://${u}`);
      }
      let protocol;
      let port;
      const host = url.hostname;
      const path = url.pathname;
      const search = url.search || "";
      switch (srcProto) {
        case "http:":
        case "ws:":
        case "nats:":
          port = url.port || "80";
          protocol = "ws:";
          break;
        case "https:":
        case "wss:":
        case "tls:":
          port = url.port || "443";
          protocol = "wss:";
          break;
        default:
          port = url.port || encrypted === true ? "443" : "80";
          protocol = encrypted === true ? "wss:" : "ws:";
          break;
      }
      return `${protocol}//${host}:${port}${path}${search}`;
    }
    function wsconnect2(opts = {}) {
      (0, transport_1.setTransportFactory)({
        defaultPort: 443,
        urlParseFn: wsUrlParseFn,
        factory: () => {
          if (opts.tls) {
            throw errors_1.InvalidArgumentError.format("tls", "is not configurable on w3c websocket connections");
          }
          return new WsTransport();
        }
      });
      return nats_1.NatsConnectionImpl.connect(opts);
    }
  }
});

// node_modules/@nats-io/nats-core/lib/internal_mod.js
var require_internal_mod = __commonJS({
  "node_modules/@nats-io/nats-core/lib/internal_mod.js"(exports) {
    "use strict";
    var __createBinding = exports && exports.__createBinding || (Object.create ? function(o, m, k, k2) {
      if (k2 === void 0) k2 = k;
      var desc = Object.getOwnPropertyDescriptor(m, k);
      if (!desc || ("get" in desc ? !m.__esModule : desc.writable || desc.configurable)) {
        desc = { enumerable: true, get: function() {
          return m[k];
        } };
      }
      Object.defineProperty(o, k2, desc);
    } : function(o, m, k, k2) {
      if (k2 === void 0) k2 = k;
      o[k2] = m[k];
    });
    var __exportStar = exports && exports.__exportStar || function(m, exports2) {
      for (var p in m) if (p !== "default" && !Object.prototype.hasOwnProperty.call(exports2, p)) __createBinding(exports2, m, p);
    };
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.Metric = exports.Bench = exports.writeAll = exports.readAll = exports.MAX_SIZE = exports.DenoBuffer = exports.State = exports.Parser = exports.Kind = exports.describe = exports.QueuedIteratorImpl = exports.usernamePasswordAuthenticator = exports.tokenAuthenticator = exports.nkeyAuthenticator = exports.jwtAuthenticator = exports.credsAuthenticator = exports.RequestOne = exports.parseOptions = exports.hasWsProtocol = exports.defaultOptions = exports.DEFAULT_MAX_RECONNECT_ATTEMPTS = exports.checkUnsupportedOption = exports.checkOptions = exports.buildAuthenticator = exports.DataBuffer = exports.MuxSubscription = exports.Heartbeat = exports.MsgHdrsImpl = exports.headers = exports.canonicalMIMEHeaderKey = exports.timeout = exports.SimpleMutex = exports.render = exports.nanos = exports.millis = exports.extend = exports.delay = exports.deferred = exports.deadline = exports.collect = exports.backoff = exports.ProtocolHandler = exports.INFO = exports.Connect = exports.setTransportFactory = exports.getResolveFn = exports.MsgImpl = exports.nuid = exports.Nuid = exports.NatsConnectionImpl = void 0;
    exports.UserAuthenticationExpiredError = exports.TimeoutError = exports.RequestError = exports.ProtocolError = exports.PermissionViolationError = exports.NoRespondersError = exports.InvalidSubjectError = exports.InvalidOperationError = exports.InvalidArgumentError = exports.errors = exports.DrainingConnectionError = exports.ConnectionError = exports.ClosedConnectionError = exports.AuthorizationError = exports.wsUrlParseFn = exports.wsconnect = exports.Servers = exports.isIPV4OrHostname = exports.IdleHeartbeatMonitor = exports.Subscriptions = exports.SubscriptionImpl = exports.syncIterator = exports.Match = exports.createInbox = exports.protoLen = exports.extractProtocolMessage = exports.Empty = exports.parseSemVer = exports.Features = exports.Feature = exports.compare = exports.parseIP = exports.isIP = exports.ipV4 = exports.TE = exports.TD = void 0;
    var nats_1 = require_nats();
    Object.defineProperty(exports, "NatsConnectionImpl", { enumerable: true, get: function() {
      return nats_1.NatsConnectionImpl;
    } });
    var nuid_1 = require_nuid2();
    Object.defineProperty(exports, "Nuid", { enumerable: true, get: function() {
      return nuid_1.Nuid;
    } });
    Object.defineProperty(exports, "nuid", { enumerable: true, get: function() {
      return nuid_1.nuid;
    } });
    var msg_1 = require_msg();
    Object.defineProperty(exports, "MsgImpl", { enumerable: true, get: function() {
      return msg_1.MsgImpl;
    } });
    var transport_1 = require_transport();
    Object.defineProperty(exports, "getResolveFn", { enumerable: true, get: function() {
      return transport_1.getResolveFn;
    } });
    Object.defineProperty(exports, "setTransportFactory", { enumerable: true, get: function() {
      return transport_1.setTransportFactory;
    } });
    var protocol_1 = require_protocol();
    Object.defineProperty(exports, "Connect", { enumerable: true, get: function() {
      return protocol_1.Connect;
    } });
    Object.defineProperty(exports, "INFO", { enumerable: true, get: function() {
      return protocol_1.INFO;
    } });
    Object.defineProperty(exports, "ProtocolHandler", { enumerable: true, get: function() {
      return protocol_1.ProtocolHandler;
    } });
    var util_1 = require_util();
    Object.defineProperty(exports, "backoff", { enumerable: true, get: function() {
      return util_1.backoff;
    } });
    Object.defineProperty(exports, "collect", { enumerable: true, get: function() {
      return util_1.collect;
    } });
    Object.defineProperty(exports, "deadline", { enumerable: true, get: function() {
      return util_1.deadline;
    } });
    Object.defineProperty(exports, "deferred", { enumerable: true, get: function() {
      return util_1.deferred;
    } });
    Object.defineProperty(exports, "delay", { enumerable: true, get: function() {
      return util_1.delay;
    } });
    Object.defineProperty(exports, "extend", { enumerable: true, get: function() {
      return util_1.extend;
    } });
    Object.defineProperty(exports, "millis", { enumerable: true, get: function() {
      return util_1.millis;
    } });
    Object.defineProperty(exports, "nanos", { enumerable: true, get: function() {
      return util_1.nanos;
    } });
    Object.defineProperty(exports, "render", { enumerable: true, get: function() {
      return util_1.render;
    } });
    Object.defineProperty(exports, "SimpleMutex", { enumerable: true, get: function() {
      return util_1.SimpleMutex;
    } });
    Object.defineProperty(exports, "timeout", { enumerable: true, get: function() {
      return util_1.timeout;
    } });
    var headers_1 = require_headers();
    Object.defineProperty(exports, "canonicalMIMEHeaderKey", { enumerable: true, get: function() {
      return headers_1.canonicalMIMEHeaderKey;
    } });
    Object.defineProperty(exports, "headers", { enumerable: true, get: function() {
      return headers_1.headers;
    } });
    Object.defineProperty(exports, "MsgHdrsImpl", { enumerable: true, get: function() {
      return headers_1.MsgHdrsImpl;
    } });
    var heartbeats_1 = require_heartbeats();
    Object.defineProperty(exports, "Heartbeat", { enumerable: true, get: function() {
      return heartbeats_1.Heartbeat;
    } });
    var muxsubscription_1 = require_muxsubscription();
    Object.defineProperty(exports, "MuxSubscription", { enumerable: true, get: function() {
      return muxsubscription_1.MuxSubscription;
    } });
    var databuffer_1 = require_databuffer();
    Object.defineProperty(exports, "DataBuffer", { enumerable: true, get: function() {
      return databuffer_1.DataBuffer;
    } });
    var options_1 = require_options();
    Object.defineProperty(exports, "buildAuthenticator", { enumerable: true, get: function() {
      return options_1.buildAuthenticator;
    } });
    Object.defineProperty(exports, "checkOptions", { enumerable: true, get: function() {
      return options_1.checkOptions;
    } });
    Object.defineProperty(exports, "checkUnsupportedOption", { enumerable: true, get: function() {
      return options_1.checkUnsupportedOption;
    } });
    Object.defineProperty(exports, "DEFAULT_MAX_RECONNECT_ATTEMPTS", { enumerable: true, get: function() {
      return options_1.DEFAULT_MAX_RECONNECT_ATTEMPTS;
    } });
    Object.defineProperty(exports, "defaultOptions", { enumerable: true, get: function() {
      return options_1.defaultOptions;
    } });
    Object.defineProperty(exports, "hasWsProtocol", { enumerable: true, get: function() {
      return options_1.hasWsProtocol;
    } });
    Object.defineProperty(exports, "parseOptions", { enumerable: true, get: function() {
      return options_1.parseOptions;
    } });
    var request_1 = require_request();
    Object.defineProperty(exports, "RequestOne", { enumerable: true, get: function() {
      return request_1.RequestOne;
    } });
    var authenticator_1 = require_authenticator();
    Object.defineProperty(exports, "credsAuthenticator", { enumerable: true, get: function() {
      return authenticator_1.credsAuthenticator;
    } });
    Object.defineProperty(exports, "jwtAuthenticator", { enumerable: true, get: function() {
      return authenticator_1.jwtAuthenticator;
    } });
    Object.defineProperty(exports, "nkeyAuthenticator", { enumerable: true, get: function() {
      return authenticator_1.nkeyAuthenticator;
    } });
    Object.defineProperty(exports, "tokenAuthenticator", { enumerable: true, get: function() {
      return authenticator_1.tokenAuthenticator;
    } });
    Object.defineProperty(exports, "usernamePasswordAuthenticator", { enumerable: true, get: function() {
      return authenticator_1.usernamePasswordAuthenticator;
    } });
    __exportStar(require_nkeys2(), exports);
    var queued_iterator_1 = require_queued_iterator();
    Object.defineProperty(exports, "QueuedIteratorImpl", { enumerable: true, get: function() {
      return queued_iterator_1.QueuedIteratorImpl;
    } });
    var parser_1 = require_parser();
    Object.defineProperty(exports, "describe", { enumerable: true, get: function() {
      return parser_1.describe;
    } });
    Object.defineProperty(exports, "Kind", { enumerable: true, get: function() {
      return parser_1.Kind;
    } });
    Object.defineProperty(exports, "Parser", { enumerable: true, get: function() {
      return parser_1.Parser;
    } });
    Object.defineProperty(exports, "State", { enumerable: true, get: function() {
      return parser_1.State;
    } });
    var denobuffer_1 = require_denobuffer();
    Object.defineProperty(exports, "DenoBuffer", { enumerable: true, get: function() {
      return denobuffer_1.DenoBuffer;
    } });
    Object.defineProperty(exports, "MAX_SIZE", { enumerable: true, get: function() {
      return denobuffer_1.MAX_SIZE;
    } });
    Object.defineProperty(exports, "readAll", { enumerable: true, get: function() {
      return denobuffer_1.readAll;
    } });
    Object.defineProperty(exports, "writeAll", { enumerable: true, get: function() {
      return denobuffer_1.writeAll;
    } });
    var bench_1 = require_bench();
    Object.defineProperty(exports, "Bench", { enumerable: true, get: function() {
      return bench_1.Bench;
    } });
    Object.defineProperty(exports, "Metric", { enumerable: true, get: function() {
      return bench_1.Metric;
    } });
    var encoders_1 = require_encoders();
    Object.defineProperty(exports, "TD", { enumerable: true, get: function() {
      return encoders_1.TD;
    } });
    Object.defineProperty(exports, "TE", { enumerable: true, get: function() {
      return encoders_1.TE;
    } });
    var ipparser_1 = require_ipparser();
    Object.defineProperty(exports, "ipV4", { enumerable: true, get: function() {
      return ipparser_1.ipV4;
    } });
    Object.defineProperty(exports, "isIP", { enumerable: true, get: function() {
      return ipparser_1.isIP;
    } });
    Object.defineProperty(exports, "parseIP", { enumerable: true, get: function() {
      return ipparser_1.parseIP;
    } });
    var semver_1 = require_semver();
    Object.defineProperty(exports, "compare", { enumerable: true, get: function() {
      return semver_1.compare;
    } });
    Object.defineProperty(exports, "Feature", { enumerable: true, get: function() {
      return semver_1.Feature;
    } });
    Object.defineProperty(exports, "Features", { enumerable: true, get: function() {
      return semver_1.Features;
    } });
    Object.defineProperty(exports, "parseSemVer", { enumerable: true, get: function() {
      return semver_1.parseSemVer;
    } });
    var types_1 = require_types();
    Object.defineProperty(exports, "Empty", { enumerable: true, get: function() {
      return types_1.Empty;
    } });
    var transport_2 = require_transport();
    Object.defineProperty(exports, "extractProtocolMessage", { enumerable: true, get: function() {
      return transport_2.extractProtocolMessage;
    } });
    Object.defineProperty(exports, "protoLen", { enumerable: true, get: function() {
      return transport_2.protoLen;
    } });
    var core_1 = require_core();
    Object.defineProperty(exports, "createInbox", { enumerable: true, get: function() {
      return core_1.createInbox;
    } });
    Object.defineProperty(exports, "Match", { enumerable: true, get: function() {
      return core_1.Match;
    } });
    Object.defineProperty(exports, "syncIterator", { enumerable: true, get: function() {
      return core_1.syncIterator;
    } });
    var protocol_2 = require_protocol();
    Object.defineProperty(exports, "SubscriptionImpl", { enumerable: true, get: function() {
      return protocol_2.SubscriptionImpl;
    } });
    Object.defineProperty(exports, "Subscriptions", { enumerable: true, get: function() {
      return protocol_2.Subscriptions;
    } });
    var idleheartbeat_monitor_1 = require_idleheartbeat_monitor();
    Object.defineProperty(exports, "IdleHeartbeatMonitor", { enumerable: true, get: function() {
      return idleheartbeat_monitor_1.IdleHeartbeatMonitor;
    } });
    var servers_1 = require_servers();
    Object.defineProperty(exports, "isIPV4OrHostname", { enumerable: true, get: function() {
      return servers_1.isIPV4OrHostname;
    } });
    Object.defineProperty(exports, "Servers", { enumerable: true, get: function() {
      return servers_1.Servers;
    } });
    var ws_transport_1 = require_ws_transport();
    Object.defineProperty(exports, "wsconnect", { enumerable: true, get: function() {
      return ws_transport_1.wsconnect;
    } });
    Object.defineProperty(exports, "wsUrlParseFn", { enumerable: true, get: function() {
      return ws_transport_1.wsUrlParseFn;
    } });
    var errors_1 = require_errors();
    Object.defineProperty(exports, "AuthorizationError", { enumerable: true, get: function() {
      return errors_1.AuthorizationError;
    } });
    Object.defineProperty(exports, "ClosedConnectionError", { enumerable: true, get: function() {
      return errors_1.ClosedConnectionError;
    } });
    Object.defineProperty(exports, "ConnectionError", { enumerable: true, get: function() {
      return errors_1.ConnectionError;
    } });
    Object.defineProperty(exports, "DrainingConnectionError", { enumerable: true, get: function() {
      return errors_1.DrainingConnectionError;
    } });
    Object.defineProperty(exports, "errors", { enumerable: true, get: function() {
      return errors_1.errors;
    } });
    Object.defineProperty(exports, "InvalidArgumentError", { enumerable: true, get: function() {
      return errors_1.InvalidArgumentError;
    } });
    Object.defineProperty(exports, "InvalidOperationError", { enumerable: true, get: function() {
      return errors_1.InvalidOperationError;
    } });
    Object.defineProperty(exports, "InvalidSubjectError", { enumerable: true, get: function() {
      return errors_1.InvalidSubjectError;
    } });
    Object.defineProperty(exports, "NoRespondersError", { enumerable: true, get: function() {
      return errors_1.NoRespondersError;
    } });
    Object.defineProperty(exports, "PermissionViolationError", { enumerable: true, get: function() {
      return errors_1.PermissionViolationError;
    } });
    Object.defineProperty(exports, "ProtocolError", { enumerable: true, get: function() {
      return errors_1.ProtocolError;
    } });
    Object.defineProperty(exports, "RequestError", { enumerable: true, get: function() {
      return errors_1.RequestError;
    } });
    Object.defineProperty(exports, "TimeoutError", { enumerable: true, get: function() {
      return errors_1.TimeoutError;
    } });
    Object.defineProperty(exports, "UserAuthenticationExpiredError", { enumerable: true, get: function() {
      return errors_1.UserAuthenticationExpiredError;
    } });
  }
});

// node_modules/@nats-io/nats-core/lib/mod.js
var require_mod3 = __commonJS({
  "node_modules/@nats-io/nats-core/lib/mod.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.wsconnect = exports.usernamePasswordAuthenticator = exports.UserAuthenticationExpiredError = exports.tokenAuthenticator = exports.TimeoutError = exports.syncIterator = exports.RequestError = exports.ProtocolError = exports.PermissionViolationError = exports.nuid = exports.Nuid = exports.NoRespondersError = exports.nkeys = exports.nkeyAuthenticator = exports.nanos = exports.MsgHdrsImpl = exports.millis = exports.Metric = exports.Match = exports.jwtAuthenticator = exports.InvalidSubjectError = exports.InvalidOperationError = exports.InvalidArgumentError = exports.headers = exports.hasWsProtocol = exports.errors = exports.Empty = exports.DrainingConnectionError = exports.delay = exports.deferred = exports.deadline = exports.credsAuthenticator = exports.createInbox = exports.ConnectionError = exports.ClosedConnectionError = exports.canonicalMIMEHeaderKey = exports.buildAuthenticator = exports.Bench = exports.backoff = exports.AuthorizationError = void 0;
    var internal_mod_1 = require_internal_mod();
    Object.defineProperty(exports, "AuthorizationError", { enumerable: true, get: function() {
      return internal_mod_1.AuthorizationError;
    } });
    Object.defineProperty(exports, "backoff", { enumerable: true, get: function() {
      return internal_mod_1.backoff;
    } });
    Object.defineProperty(exports, "Bench", { enumerable: true, get: function() {
      return internal_mod_1.Bench;
    } });
    Object.defineProperty(exports, "buildAuthenticator", { enumerable: true, get: function() {
      return internal_mod_1.buildAuthenticator;
    } });
    Object.defineProperty(exports, "canonicalMIMEHeaderKey", { enumerable: true, get: function() {
      return internal_mod_1.canonicalMIMEHeaderKey;
    } });
    Object.defineProperty(exports, "ClosedConnectionError", { enumerable: true, get: function() {
      return internal_mod_1.ClosedConnectionError;
    } });
    Object.defineProperty(exports, "ConnectionError", { enumerable: true, get: function() {
      return internal_mod_1.ConnectionError;
    } });
    Object.defineProperty(exports, "createInbox", { enumerable: true, get: function() {
      return internal_mod_1.createInbox;
    } });
    Object.defineProperty(exports, "credsAuthenticator", { enumerable: true, get: function() {
      return internal_mod_1.credsAuthenticator;
    } });
    Object.defineProperty(exports, "deadline", { enumerable: true, get: function() {
      return internal_mod_1.deadline;
    } });
    Object.defineProperty(exports, "deferred", { enumerable: true, get: function() {
      return internal_mod_1.deferred;
    } });
    Object.defineProperty(exports, "delay", { enumerable: true, get: function() {
      return internal_mod_1.delay;
    } });
    Object.defineProperty(exports, "DrainingConnectionError", { enumerable: true, get: function() {
      return internal_mod_1.DrainingConnectionError;
    } });
    Object.defineProperty(exports, "Empty", { enumerable: true, get: function() {
      return internal_mod_1.Empty;
    } });
    Object.defineProperty(exports, "errors", { enumerable: true, get: function() {
      return internal_mod_1.errors;
    } });
    Object.defineProperty(exports, "hasWsProtocol", { enumerable: true, get: function() {
      return internal_mod_1.hasWsProtocol;
    } });
    Object.defineProperty(exports, "headers", { enumerable: true, get: function() {
      return internal_mod_1.headers;
    } });
    Object.defineProperty(exports, "InvalidArgumentError", { enumerable: true, get: function() {
      return internal_mod_1.InvalidArgumentError;
    } });
    Object.defineProperty(exports, "InvalidOperationError", { enumerable: true, get: function() {
      return internal_mod_1.InvalidOperationError;
    } });
    Object.defineProperty(exports, "InvalidSubjectError", { enumerable: true, get: function() {
      return internal_mod_1.InvalidSubjectError;
    } });
    Object.defineProperty(exports, "jwtAuthenticator", { enumerable: true, get: function() {
      return internal_mod_1.jwtAuthenticator;
    } });
    Object.defineProperty(exports, "Match", { enumerable: true, get: function() {
      return internal_mod_1.Match;
    } });
    Object.defineProperty(exports, "Metric", { enumerable: true, get: function() {
      return internal_mod_1.Metric;
    } });
    Object.defineProperty(exports, "millis", { enumerable: true, get: function() {
      return internal_mod_1.millis;
    } });
    Object.defineProperty(exports, "MsgHdrsImpl", { enumerable: true, get: function() {
      return internal_mod_1.MsgHdrsImpl;
    } });
    Object.defineProperty(exports, "nanos", { enumerable: true, get: function() {
      return internal_mod_1.nanos;
    } });
    Object.defineProperty(exports, "nkeyAuthenticator", { enumerable: true, get: function() {
      return internal_mod_1.nkeyAuthenticator;
    } });
    Object.defineProperty(exports, "nkeys", { enumerable: true, get: function() {
      return internal_mod_1.nkeys;
    } });
    Object.defineProperty(exports, "NoRespondersError", { enumerable: true, get: function() {
      return internal_mod_1.NoRespondersError;
    } });
    Object.defineProperty(exports, "Nuid", { enumerable: true, get: function() {
      return internal_mod_1.Nuid;
    } });
    Object.defineProperty(exports, "nuid", { enumerable: true, get: function() {
      return internal_mod_1.nuid;
    } });
    Object.defineProperty(exports, "PermissionViolationError", { enumerable: true, get: function() {
      return internal_mod_1.PermissionViolationError;
    } });
    Object.defineProperty(exports, "ProtocolError", { enumerable: true, get: function() {
      return internal_mod_1.ProtocolError;
    } });
    Object.defineProperty(exports, "RequestError", { enumerable: true, get: function() {
      return internal_mod_1.RequestError;
    } });
    Object.defineProperty(exports, "syncIterator", { enumerable: true, get: function() {
      return internal_mod_1.syncIterator;
    } });
    Object.defineProperty(exports, "TimeoutError", { enumerable: true, get: function() {
      return internal_mod_1.TimeoutError;
    } });
    Object.defineProperty(exports, "tokenAuthenticator", { enumerable: true, get: function() {
      return internal_mod_1.tokenAuthenticator;
    } });
    Object.defineProperty(exports, "UserAuthenticationExpiredError", { enumerable: true, get: function() {
      return internal_mod_1.UserAuthenticationExpiredError;
    } });
    Object.defineProperty(exports, "usernamePasswordAuthenticator", { enumerable: true, get: function() {
      return internal_mod_1.usernamePasswordAuthenticator;
    } });
    Object.defineProperty(exports, "wsconnect", { enumerable: true, get: function() {
      return internal_mod_1.wsconnect;
    } });
  }
});

// node_modules/@nats-io/jetstream/lib/types.js
var require_types2 = __commonJS({
  "node_modules/@nats-io/jetstream/lib/types.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.RepublishHeaders = exports.DirectMsgHeaders = exports.JsHeaders = exports.AdvisoryKind = void 0;
    exports.isOrderedPushConsumerOptions = isOrderedPushConsumerOptions;
    exports.isPullConsumer = isPullConsumer;
    exports.isPushConsumer = isPushConsumer;
    exports.isBoundPushConsumerOptions = isBoundPushConsumerOptions;
    function isOrderedPushConsumerOptions(v) {
      if (v && typeof v === "object") {
        return "name_prefix" in v || "deliver_subject_prefix" in v || "filter_subjects" in v || "filter_subject" in v || "deliver_policy" in v || "opt_start_seq" in v || "opt_start_time" in v || "replay_policy" in v || "inactive_threshold" in v || "headers_only" in v || "deliver_prefix" in v;
      }
      return false;
    }
    function isPullConsumer(v) {
      return v.isPullConsumer();
    }
    function isPushConsumer(v) {
      return v.isPushConsumer();
    }
    function isBoundPushConsumerOptions(v) {
      if (v && typeof v === "object") {
        return "deliver_subject" in v || "deliver_group" in v || "idle_heartbeat" in v;
      }
      return false;
    }
    exports.AdvisoryKind = {
      API: "api_audit",
      StreamAction: "stream_action",
      ConsumerAction: "consumer_action",
      SnapshotCreate: "snapshot_create",
      SnapshotComplete: "snapshot_complete",
      RestoreCreate: "restore_create",
      RestoreComplete: "restore_complete",
      MaxDeliver: "max_deliver",
      Terminated: "terminated",
      Ack: "consumer_ack",
      StreamLeaderElected: "stream_leader_elected",
      StreamQuorumLost: "stream_quorum_lost",
      ConsumerLeaderElected: "consumer_leader_elected",
      ConsumerQuorumLost: "consumer_quorum_lost"
    };
    exports.JsHeaders = {
      /**
       * Set if message is from a stream source - format is `stream seq`
       */
      StreamSourceHdr: "Nats-Stream-Source",
      /**
       * Set for heartbeat messages
       */
      LastConsumerSeqHdr: "Nats-Last-Consumer",
      /**
       * Set for heartbeat messages
       */
      LastStreamSeqHdr: "Nats-Last-Stream",
      /**
       * Set for heartbeat messages if the consumer is stalled, reply subject
       * will unstall the client when the client responds
       */
      ConsumerStalledHdr: "Nats-Consumer-Stalled",
      /**
       * Set for headers_only consumers indicates the number of bytes in the payload
       */
      MessageSizeHdr: "Nats-Msg-Size",
      // rollup header
      RollupHdr: "Nats-Rollup",
      // value for rollup header when rolling up a subject
      RollupValueSubject: "sub",
      // value for rollup header when rolling up all subjects
      RollupValueAll: "all",
      /**
       * Set on protocol messages to indicate pull request message count that
       * was not honored.
       */
      PendingMessagesHdr: "Nats-Pending-Messages",
      /**
       * Set on protocol messages to indicate pull request byte count that
       * was not honored
       */
      PendingBytesHdr: "Nats-Pending-Bytes",
      /**
       * Asserts a minimum JetStream API level on a JS API request (ADR-44).
       */
      RequiredApiLevel: "Nats-Required-Api-Level"
    };
    exports.DirectMsgHeaders = {
      Stream: "Nats-Stream",
      Sequence: "Nats-Sequence",
      TimeStamp: "Nats-Time-Stamp",
      Subject: "Nats-Subject",
      LastSequence: "Nats-Last-Sequence",
      NumPending: "Nats-Num-Pending"
    };
    exports.RepublishHeaders = {
      /**
       * The source stream of the message
       */
      Stream: "Nats-Stream",
      /**
       * The original subject of the message
       */
      Subject: "Nats-Subject",
      /**
       * The sequence of the republished message
       */
      Sequence: "Nats-Sequence",
      /**
       * The stream sequence id of the last message ingested to the same original subject (or 0 if none or deleted)
       */
      LastSequence: "Nats-Last-Sequence",
      /**
       * The size in bytes of the message's body - Only if {@link Republish#headers_only} is set.
       */
      Size: "Nats-Msg-Size"
    };
  }
});

// node_modules/@nats-io/jetstream/lib/jserrors.js
var require_jserrors = __commonJS({
  "node_modules/@nats-io/jetstream/lib/jserrors.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.jserrors = exports.StreamNotFoundError = exports.ConsumerNotFoundError = exports.JetStreamApiError = exports.InvalidNameError = exports.JetStreamApiCodes = exports.JetStreamStatus = exports.JetStreamStatusError = exports.JetStreamError = exports.JetStreamNotEnabled = void 0;
    exports.isMessageNotFound = isMessageNotFound;
    var types_1 = require_types2();
    var JetStreamNotEnabled = class extends Error {
      constructor(message, opts) {
        super(message, opts);
        this.name = "JetStreamNotEnabled";
      }
    };
    exports.JetStreamNotEnabled = JetStreamNotEnabled;
    var JetStreamError = class extends Error {
      constructor(message, opts) {
        super(message, opts);
        this.name = "JetStreamError";
      }
    };
    exports.JetStreamError = JetStreamError;
    var JetStreamStatusError = class extends JetStreamError {
      code;
      constructor(message, code, opts) {
        super(message, opts);
        this.code = code;
        this.name = "JetStreamStatusError";
      }
    };
    exports.JetStreamStatusError = JetStreamStatusError;
    var JetStreamStatus = class _JetStreamStatus {
      msg;
      _description;
      constructor(msg) {
        this.msg = msg;
        this._description = "";
      }
      static maybeParseStatus(msg) {
        const status = new _JetStreamStatus(msg);
        return status.code === 0 ? null : status;
      }
      toError() {
        return new JetStreamStatusError(this.description, this.code);
      }
      debug() {
        console.log({
          subject: this.msg.subject,
          reply: this.msg.reply,
          description: this.description,
          status: this.code,
          headers: this.msg.headers
        });
      }
      get code() {
        return this.msg.headers?.code || 0;
      }
      get description() {
        if (this._description === "") {
          this._description = this.msg.headers?.description?.toLowerCase() || "";
          if (this._description === "") {
            this._description = this.code === 503 ? "no responders" : "unknown";
          }
        }
        return this._description;
      }
      isIdleHeartbeat() {
        return this.code === 100 && this.description === "idle heartbeat";
      }
      isFlowControlRequest() {
        return this.code === 100 && this.description === "flowcontrol request";
      }
      parseHeartbeat() {
        if (this.isIdleHeartbeat()) {
          return {
            type: "heartbeat",
            lastConsumerSequence: parseInt(this.msg.headers?.get("Nats-Last-Consumer") || "0"),
            lastStreamSequence: parseInt(this.msg.headers?.get("Nats-Last-Stream") || "0")
          };
        }
        return null;
      }
      isRequestTimeout() {
        return this.code === 408 && this.description === "request timeout";
      }
      parseDiscard() {
        const discard = {
          msgsLeft: 0,
          bytesLeft: 0
        };
        const msgsLeft = this.msg.headers?.get(types_1.JsHeaders.PendingMessagesHdr);
        if (msgsLeft) {
          discard.msgsLeft = parseInt(msgsLeft);
        }
        const bytesLeft = this.msg.headers?.get(types_1.JsHeaders.PendingBytesHdr);
        if (bytesLeft) {
          discard.bytesLeft = parseInt(bytesLeft);
        }
        return discard;
      }
      isBadRequest() {
        return this.code === 400;
      }
      isConsumerDeleted() {
        return this.code === 409 && this.description === "consumer deleted";
      }
      isStreamDeleted() {
        return this.code === 409 && this.description === "stream deleted";
      }
      isIdleHeartbeatMissed() {
        return this.code === 409 && this.description === "idle heartbeats missed";
      }
      isMaxWaitingExceeded() {
        return this.code === 409 && this.description === "exceeded maxwaiting";
      }
      isConsumerIsPushBased() {
        return this.code === 409 && this.description === "consumer is push based";
      }
      isExceededMaxWaiting() {
        return this.code === 409 && this.description.includes("exceeded maxwaiting");
      }
      isExceededMaxRequestBatch() {
        return this.code === 409 && this.description.includes("exceeded maxrequestbatch");
      }
      isExceededMaxExpires() {
        return this.code === 409 && this.description.includes("exceeded maxrequestexpires");
      }
      isExceededLimit() {
        return this.isExceededMaxExpires() || this.isExceededMaxWaiting() || this.isExceededMaxRequestBatch() || this.isMessageSizeExceedsMaxBytes();
      }
      isMessageNotFound() {
        return this.code === 404 && this.description === "message not found";
      }
      isNoResults() {
        return this.code === 404 && this.description === "no results";
      }
      isMessageSizeExceedsMaxBytes() {
        return this.code === 409 && this.description === "message size exceeds maxbytes";
      }
      isEndOfBatch() {
        return this.code === 204 && this.description === "eob";
      }
    };
    exports.JetStreamStatus = JetStreamStatus;
    exports.JetStreamApiCodes = {
      ConsumerNotFound: 10014,
      StreamNotFound: 10059,
      JetStreamNotEnabledForAccount: 10039,
      StreamWrongLastSequence: 10071,
      StreamWrongLastSequenceUnknown: 10164,
      NoMessageFound: 10037
    };
    function isMessageNotFound(err) {
      return err instanceof JetStreamApiError && err.code === exports.JetStreamApiCodes.NoMessageFound;
    }
    var InvalidNameError = class extends Error {
      constructor(message = "", opts) {
        super(message, opts);
        this.name = "InvalidNameError";
      }
    };
    exports.InvalidNameError = InvalidNameError;
    var JetStreamApiError = class extends Error {
      #apiError;
      constructor(jsErr, opts) {
        super(jsErr.description, opts);
        this.#apiError = jsErr;
        this.name = "JetStreamApiError";
      }
      get code() {
        return this.#apiError.err_code;
      }
      get status() {
        return this.#apiError.code;
      }
      apiError() {
        return Object.assign({}, this.#apiError);
      }
    };
    exports.JetStreamApiError = JetStreamApiError;
    var ConsumerNotFoundError = class extends JetStreamApiError {
      constructor(jsErr, opts) {
        super(jsErr, opts);
        this.name = "ConsumerNotFoundError";
      }
    };
    exports.ConsumerNotFoundError = ConsumerNotFoundError;
    var StreamNotFoundError = class _StreamNotFoundError extends JetStreamApiError {
      constructor(jsErr, opts) {
        super(jsErr, opts);
        this.name = "StreamNotFoundError";
      }
      static fromMessage(message) {
        return new _StreamNotFoundError({
          err_code: exports.JetStreamApiCodes.StreamNotFound,
          description: message,
          code: 404
        });
      }
    };
    exports.StreamNotFoundError = StreamNotFoundError;
    exports.jserrors = {
      InvalidNameError,
      ConsumerNotFoundError,
      StreamNotFoundError,
      JetStreamError,
      JetStreamApiError,
      JetStreamNotEnabled
    };
  }
});

// node_modules/@nats-io/jetstream/lib/jsbaseclient_api.js
var require_jsbaseclient_api = __commonJS({
  "node_modules/@nats-io/jetstream/lib/jsbaseclient_api.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.BaseApiClientImpl = void 0;
    exports.parseJsResponse = parseJsResponse;
    exports.defaultJsOptions = defaultJsOptions;
    var internal_1 = require_internal_mod();
    var types_1 = require_types2();
    var jserrors_1 = require_jserrors();
    var defaultPrefix = "$JS.API";
    var defaultTimeout = 5e3;
    function parseJsResponse(m) {
      const v = JSON.parse(new TextDecoder().decode(m.data));
      const r = v;
      if (r.error) {
        switch (r.error.err_code) {
          case jserrors_1.JetStreamApiCodes.ConsumerNotFound:
            throw new jserrors_1.ConsumerNotFoundError(r.error);
          case jserrors_1.JetStreamApiCodes.StreamNotFound:
            throw new jserrors_1.StreamNotFoundError(r.error);
          case jserrors_1.JetStreamApiCodes.JetStreamNotEnabledForAccount: {
            const jserr = new jserrors_1.JetStreamApiError(r.error);
            throw new jserrors_1.JetStreamNotEnabled(jserr.message, { cause: jserr });
          }
          default:
            throw new jserrors_1.JetStreamApiError(r.error);
        }
      }
      return v;
    }
    function defaultJsOptions(opts) {
      opts = opts || {};
      if (opts.domain) {
        opts.apiPrefix = `$JS.${opts.domain}.API`;
        delete opts.domain;
      }
      return (0, internal_1.extend)({ apiPrefix: defaultPrefix, timeout: defaultTimeout }, opts);
    }
    var BaseApiClientImpl = class {
      nc;
      opts;
      prefix;
      timeout;
      constructor(nc, opts) {
        this.nc = nc;
        opts = opts || {};
        opts.watcherPrefix = opts.watcherPrefix || this.nc.options.inboxPrefix;
        this.opts = defaultJsOptions(opts);
        this._parseOpts();
        this.prefix = this.opts.apiPrefix;
        this.timeout = this.opts.timeout;
      }
      getOptions() {
        return Object.assign({}, this.opts);
      }
      sendRequiredApiLevel() {
        return this.opts.sendRequiredApiLevel === true;
      }
      _parseOpts() {
        let prefix = this.opts.apiPrefix;
        if (!prefix || prefix.length === 0) {
          throw internal_1.errors.InvalidArgumentError.format("prefix", "cannot be empty");
        }
        const c = prefix[prefix.length - 1];
        if (c === ".") {
          prefix = prefix.substr(0, prefix.length - 1);
        }
        this.opts.apiPrefix = prefix;
        (0, internal_1.createInbox)(this.opts.watcherPrefix);
      }
      async _request(subj, data = null, opts) {
        const { retries: r, minApiVersion, ...rest } = opts ?? {};
        const reqOpts = { ...rest, timeout: this.timeout };
        let a = internal_1.Empty;
        if (data) {
          a = new TextEncoder().encode(JSON.stringify(data));
        }
        if (typeof minApiVersion === "number") {
          const h = reqOpts.headers ?? (0, internal_1.headers)();
          h.set(types_1.JsHeaders.RequiredApiLevel, minApiVersion.toString());
          reqOpts.headers = h;
        }
        let retries = r || 1;
        retries = retries === -1 ? Number.MAX_SAFE_INTEGER : retries;
        const bo = (0, internal_1.backoff)();
        for (let i = 0; i < retries; i++) {
          try {
            const m = await this.nc.request(subj, a, reqOpts);
            return this.parseJsResponse(m);
          } catch (err) {
            const re = err instanceof internal_1.RequestError ? err : null;
            if ((err instanceof internal_1.errors.TimeoutError || re?.isNoResponders()) && i + 1 < retries) {
              await (0, internal_1.delay)(bo.backoff(i));
            } else {
              throw re?.isNoResponders() ? new jserrors_1.JetStreamNotEnabled("jetstream is not enabled", {
                cause: err
              }) : err;
            }
          }
        }
      }
      async findStream(subject) {
        const q = { subject };
        const r = await this._request(`${this.prefix}.STREAM.NAMES`, q);
        const names = r;
        if (!names.streams || names.streams.length !== 1) {
          throw jserrors_1.StreamNotFoundError.fromMessage("no stream matches subject");
        }
        return names.streams[0];
      }
      getConnection() {
        return this.nc;
      }
      parseJsResponse(m) {
        return parseJsResponse(m);
      }
    };
    exports.BaseApiClientImpl = BaseApiClientImpl;
  }
});

// node_modules/@nats-io/jetstream/lib/jslister.js
var require_jslister = __commonJS({
  "node_modules/@nats-io/jetstream/lib/jslister.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.ListerImpl = void 0;
    var internal_1 = require_internal_mod();
    var ListerImpl = class {
      err;
      offset;
      pageInfo;
      subject;
      jsm;
      filter;
      payload;
      constructor(subject, filter, jsm, payload) {
        if (!subject) {
          throw internal_1.errors.InvalidArgumentError.format("subject", "is required");
        }
        this.subject = subject;
        this.jsm = jsm;
        this.offset = 0;
        this.pageInfo = {};
        this.filter = filter;
        this.payload = payload || {};
      }
      async next() {
        if (this.err) {
          return [];
        }
        if (this.pageInfo && this.offset >= this.pageInfo.total) {
          return [];
        }
        const offset = { offset: this.offset };
        if (this.payload) {
          Object.assign(offset, this.payload);
        }
        try {
          const r = await this.jsm._request(this.subject, offset, { timeout: this.jsm.timeout });
          this.pageInfo = r;
          const count = this.countResponse(r);
          if (count === 0) {
            return [];
          }
          this.offset += count;
          return this.filter(r);
        } catch (err) {
          this.err = err;
          throw err;
        }
      }
      countResponse(r) {
        switch (r?.type) {
          case "io.nats.jetstream.api.v1.stream_names_response":
          case "io.nats.jetstream.api.v1.stream_list_response":
            return r.streams?.length || 0;
          case "io.nats.jetstream.api.v1.consumer_list_response":
            return r.consumers?.length || 0;
          default:
            console.error(`jslister.ts: unknown API response for paged output: ${r?.type}`);
            return r.streams?.length || 0;
        }
      }
      async *[Symbol.asyncIterator]() {
        let page = await this.next();
        while (page.length > 0) {
          for (const item of page) {
            yield item;
          }
          page = await this.next();
        }
      }
    };
    exports.ListerImpl = ListerImpl;
  }
});

// node_modules/@nats-io/jetstream/lib/jsutil.js
var require_jsutil = __commonJS({
  "node_modules/@nats-io/jetstream/lib/jsutil.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.validateDurableName = validateDurableName;
    exports.validateStreamName = validateStreamName;
    exports.minValidation = minValidation;
    exports.validateName = validateName;
    exports.validName = validName;
    var jserrors_1 = require_jserrors();
    function validateDurableName(name) {
      return minValidation("durable", name);
    }
    function validateStreamName(name) {
      return minValidation("stream", name);
    }
    function minValidation(context, name = "") {
      if (name === "") {
        throw Error(`${context} name required`);
      }
      const bad = [".", "*", ">", "/", "\\", " ", "	", "\n", "\r"];
      bad.forEach((v) => {
        if (name.indexOf(v) !== -1) {
          switch (v) {
            case "\n":
              v = "\\n";
              break;
            case "\r":
              v = "\\r";
              break;
            case "	":
              v = "\\t";
              break;
            default:
          }
          throw new jserrors_1.InvalidNameError(`${context} name ('${name}') cannot contain '${v}'`);
        }
      });
      return "";
    }
    function validateName(context, name = "") {
      if (name === "") {
        throw Error(`${context} name required`);
      }
      const m = validName(name);
      if (m.length) {
        throw new Error(`invalid ${context} name - ${context} name ${m}`);
      }
    }
    function validName(name = "") {
      if (name === "") {
        throw Error(`name required`);
      }
      const RE = /^[-\w]+$/g;
      const m = name.match(RE);
      if (m === null) {
        for (const c of name.split("")) {
          const mm = c.match(RE);
          if (mm === null) {
            return `cannot contain '${c}'`;
          }
        }
      }
      return "";
    }
  }
});

// node_modules/@nats-io/jetstream/lib/jsapi_types.js
var require_jsapi_types = __commonJS({
  "node_modules/@nats-io/jetstream/lib/jsapi_types.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.PubHeaders = exports.PriorityPolicy = exports.ConsumerApiAction = exports.PersistMode = exports.StoreCompression = exports.ReplayPolicy = exports.AckPolicy = exports.DeliverPolicy = exports.StorageType = exports.DiscardPolicy = exports.RetentionPolicy = void 0;
    exports.defaultConsumer = defaultConsumer;
    var nats_core_1 = require_mod3();
    exports.RetentionPolicy = {
      /**
       * Retain messages until the limits are reached, then trigger the discard policy.
       */
      Limits: "limits",
      /**
       * Retain messages while there is consumer interest on the particular subject.
       */
      Interest: "interest",
      /**
       * Retain messages until acknowledged
       */
      Workqueue: "workqueue"
    };
    exports.DiscardPolicy = {
      /**
       * Discard old messages to make room for the new ones
       */
      Old: "old",
      /**
       * Discard the new messages
       */
      New: "new"
    };
    exports.StorageType = {
      /**
       * Store persistently on files
       */
      File: "file",
      /**
       * Store in server memory - doesn't survive server restarts
       */
      Memory: "memory"
    };
    exports.DeliverPolicy = {
      /**
       * Deliver all messages
       */
      All: "all",
      /**
       * Deliver starting with the last message
       */
      Last: "last",
      /**
       * Deliver starting with new messages
       */
      New: "new",
      /**
       * Deliver starting with the specified sequence
       */
      StartSequence: "by_start_sequence",
      /**
       * Deliver starting with the specified time
       */
      StartTime: "by_start_time",
      /**
       * Deliver starting with the last messages for every subject
       */
      LastPerSubject: "last_per_subject"
    };
    exports.AckPolicy = {
      /**
       * Messages don't need to be Ack'ed.
       */
      None: "none",
      /**
       * Ack, acknowledges all messages with a lower sequence
       */
      All: "all",
      /**
       * All sequences must be explicitly acknowledged
       */
      Explicit: "explicit",
      /**
       * Functions like AckAll, but acks based on flow control responses. Used
       * for durable mirror/source consumers (ADR-60). Available on server 2.14+.
       */
      FlowControl: "flow_control",
      /**
       * @ignore
       */
      NotSet: ""
    };
    exports.ReplayPolicy = {
      /**
       * Replays messages as fast as possible
       */
      Instant: "instant",
      /**
       * Replays messages following the original delay between messages
       */
      Original: "original"
    };
    exports.StoreCompression = {
      /**
       * No compression
       */
      None: "none",
      /**
       * S2 compression
       */
      S2: "s2"
    };
    exports.PersistMode = {
      /**
       * All writes are committed and stream data is synced to disk before the publish
       * acknowledgement is sent.
       * This is the default mode, and provides the strongest data durability guarantee.
       */
      Default: "default",
      /**
       * Writes to the stream are committed, but writes to the disk are asynchronously synced.
       * The publish acknowledgement is sent before the sync to the disk is complete.
       * This could result in data-loss if the server crashes before the sync is completed, however
       * with an R3+ stream, the replication provides in-flight redundancy to reduce the likelihood of
       * this occurring with distinct fault domains.
       * This can significantly increase the publish throughput.
       */
      Async: "async"
    };
    exports.ConsumerApiAction = {
      CreateOrUpdate: "",
      Update: "update",
      Create: "create"
    };
    exports.PriorityPolicy = {
      None: "none",
      Overflow: "overflow",
      PinnedClient: "pinned_client",
      Prioritized: "prioritized"
    };
    function defaultConsumer(name, opts = {}) {
      return Object.assign({
        name,
        deliver_policy: exports.DeliverPolicy.All,
        ack_policy: exports.AckPolicy.Explicit,
        ack_wait: (0, nats_core_1.nanos)(30 * 1e3),
        replay_policy: exports.ReplayPolicy.Instant
      }, opts);
    }
    exports.PubHeaders = {
      MsgIdHdr: "Nats-Msg-Id",
      ExpectedStreamHdr: "Nats-Expected-Stream",
      ExpectedLastSeqHdr: "Nats-Expected-Last-Sequence",
      ExpectedLastMsgIdHdr: "Nats-Expected-Last-Msg-Id",
      ExpectedLastSubjectSequenceHdr: "Nats-Expected-Last-Subject-Sequence",
      ExpectedLastSubjectSequenceSubjectHdr: "Nats-Expected-Last-Subject-Sequence-Subject",
      /**
       * Sets the TTL for a message (Nanos value). Only have effect on streams that
       * enable `StreamConfig.allow_msg_ttl`.
       */
      MessageTTL: "Nats-TTL",
      Schedule: "Nats-Schedule",
      ScheduleTarget: "Nats-Schedule-Target",
      ScheduleSource: "Nats-Schedule-Source",
      ScheduleTTL: "Nats-Schedule-TTL",
      ScheduleTimeZone: "Nats-Schedule-Time-Zone",
      ScheduleRollup: "Nats-Schedule-Rollup",
      /**
       * Set on messages produced by the scheduler. Holds the subject of the
       * schedule that produced the message. Also used by clients to atomically
       * cancel a schedule (set together with `ScheduleNext: "purge"`).
       */
      Scheduler: "Nats-Scheduler",
      /**
       * Set on messages produced by the scheduler. Holds the timestamp of the
       * next invocation for cron schedules, or `purge` for delayed messages.
       * Also used by clients with value `purge` to atomically cancel a schedule.
       */
      ScheduleNext: "Nats-Schedule-Next"
    };
  }
});

// node_modules/@nats-io/jetstream/lib/jsmconsumer_api.js
var require_jsmconsumer_api = __commonJS({
  "node_modules/@nats-io/jetstream/lib/jsmconsumer_api.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.ConsumerAPIImpl = void 0;
    var jsbaseclient_api_1 = require_jsbaseclient_api();
    var jslister_1 = require_jslister();
    var jsutil_1 = require_jsutil();
    var internal_1 = require_internal_mod();
    var jsapi_types_1 = require_jsapi_types();
    var ConsumerAPIImpl = class extends jsbaseclient_api_1.BaseApiClientImpl {
      constructor(nc, opts) {
        super(nc, opts);
      }
      async addUpdate(stream, cfg, opts, delta) {
        (0, jsutil_1.validateStreamName)(stream);
        opts = opts || {};
        if (cfg.deliver_group && cfg.flow_control) {
          throw internal_1.InvalidArgumentError.format(["flow_control", "deliver_group"], "are mutually exclusive");
        }
        if (cfg.deliver_group && cfg.idle_heartbeat) {
          throw internal_1.InvalidArgumentError.format(["idle_heartbeat", "deliver_group"], "are mutually exclusive");
        }
        if (isPriorityGroup(cfg)) {
          const { min: min2, ok } = this.nc.features.get(internal_1.Feature.JS_PRIORITY_GROUPS);
          if (!ok) {
            throw new Error(`priority_groups require server ${min2}`);
          }
          if (cfg.deliver_subject) {
            throw internal_1.InvalidArgumentError.format("deliver_subject", "cannot be set when using priority groups");
          }
          validatePriorityGroups(cfg);
        }
        const cr = {};
        cr.config = cfg;
        cr.stream_name = stream;
        cr.action = opts.action || jsapi_types_1.ConsumerApiAction.Create;
        cr.pedantic = opts.pedantic || false;
        if (cr.config.durable_name) {
          (0, jsutil_1.validateDurableName)(cr.config.durable_name);
        }
        const nci = this.nc;
        let { min, ok: newAPI } = nci.features.get(internal_1.Feature.JS_NEW_CONSUMER_CREATE_API);
        const name = cfg.name === "" ? void 0 : cfg.name;
        if (name && !newAPI) {
          throw internal_1.InvalidArgumentError.format("name", `requires server ${min}`);
        }
        if (name) {
          try {
            (0, jsutil_1.minValidation)("name", name);
          } catch (err) {
            const m = err.message;
            const idx = m.indexOf("cannot contain");
            if (idx !== -1) {
              throw new Error(`consumer 'name' ${m.substring(idx)}`);
            }
            throw err;
          }
        }
        let subj;
        let consumerName = "";
        if (Array.isArray(cfg.filter_subjects)) {
          const { min: min2, ok } = nci.features.get(internal_1.Feature.JS_MULTIPLE_CONSUMER_FILTER);
          if (!ok) {
            throw internal_1.InvalidArgumentError.format("filter_subjects", `requires server ${min2}`);
          }
          newAPI = false;
        }
        if (cfg.metadata) {
          const { min: min2, ok } = nci.features.get(internal_1.Feature.JS_STREAM_CONSUMER_METADATA);
          if (!ok) {
            throw internal_1.InvalidArgumentError.format("metadata", `requires server ${min2}`);
          }
        }
        if (newAPI) {
          consumerName = cfg.name ?? cfg.durable_name ?? "";
        }
        if (consumerName !== "") {
          let fs = cfg.filter_subject ?? void 0;
          if (fs === ">") {
            fs = void 0;
          }
          subj = fs !== void 0 ? `${this.prefix}.CONSUMER.CREATE.${stream}.${consumerName}.${fs}` : `${this.prefix}.CONSUMER.CREATE.${stream}.${consumerName}`;
        } else {
          subj = cfg.durable_name ? `${this.prefix}.CONSUMER.DURABLE.CREATE.${stream}.${cfg.durable_name}` : `${this.prefix}.CONSUMER.CREATE.${stream}`;
        }
        const assertCfg = delta ?? cr.config;
        const r = await this._request(subj, cr, { ...opts, ...this.requiredApiOpts(assertCfg) });
        return r;
      }
      // mirrors server/jetstream_versioning.go:setStaticConsumerMetadata
      minConsumerApi(c) {
        if (c.ack_policy === jsapi_types_1.AckPolicy.FlowControl)
          return 4;
        if (typeof c.pause_until === "string" && c.pause_until !== "")
          return 1;
        if (c.priority_policy !== void 0 && c.priority_policy !== jsapi_types_1.PriorityPolicy.None)
          return 1;
        if (typeof c.priority_timeout === "number" && c.priority_timeout > 0) {
          return 1;
        }
        if (Array.isArray(c.priority_groups) && c.priority_groups.length > 0) {
          return 1;
        }
        return 0;
      }
      requiredApiOpts(c) {
        if (!this.sendRequiredApiLevel())
          return {};
        const minApiVersion = this.minConsumerApi(c);
        return minApiVersion > 0 ? { minApiVersion } : {};
      }
      add(stream, cfg, opts) {
        opts = opts || {};
        let action = jsapi_types_1.ConsumerApiAction.Create;
        if (typeof opts === "string") {
          action = opts;
          opts = {};
        }
        const cco = Object.assign({}, { action }, opts);
        return this.addUpdate(stream, cfg, cco);
      }
      async update(stream, durable, cfg) {
        const ci = await this.info(stream, durable);
        const changable = cfg;
        return this.addUpdate(stream, Object.assign(ci.config, changable), { action: jsapi_types_1.ConsumerApiAction.Update }, changable);
      }
      async info(stream, name) {
        (0, jsutil_1.validateStreamName)(stream);
        (0, jsutil_1.validateDurableName)(name);
        const r = await this._request(`${this.prefix}.CONSUMER.INFO.${stream}.${name}`);
        return r;
      }
      async delete(stream, name) {
        (0, jsutil_1.validateStreamName)(stream);
        (0, jsutil_1.validateDurableName)(name);
        const r = await this._request(`${this.prefix}.CONSUMER.DELETE.${stream}.${name}`);
        const cr = r;
        return cr.success;
      }
      list(stream) {
        (0, jsutil_1.validateStreamName)(stream);
        const filter = (v) => {
          const clr = v;
          return clr.consumers;
        };
        const subj = `${this.prefix}.CONSUMER.LIST.${stream}`;
        return new jslister_1.ListerImpl(subj, filter, this);
      }
      // Fixme: the API returns the number of nanoseconds, but really should return
      //  millis,
      pause(stream, name, until) {
        const subj = `${this.prefix}.CONSUMER.PAUSE.${stream}.${name}`;
        const opts = {
          pause_until: until.toISOString()
        };
        return this._request(subj, opts);
      }
      // Fixme: the API returns the number of nanoseconds, but really should return
      //  millis,
      resume(stream, name) {
        return this.pause(stream, name, /* @__PURE__ */ new Date(0));
      }
      unpin(stream, name, group) {
        const subj = `${this.prefix}.CONSUMER.UNPIN.${stream}.${name}`;
        return this._request(subj, { group });
      }
      async reset(stream, name, seq) {
        (0, jsutil_1.validateStreamName)(stream);
        (0, jsutil_1.validateDurableName)(name);
        const nci = this.nc;
        const { min, ok } = nci.features.get(internal_1.Feature.JS_CONSUMER_RESET);
        if (!ok) {
          throw new Error(`consumer reset requires server ${min}`);
        }
        if (typeof seq === "number" && (!Number.isInteger(seq) || seq < 0)) {
          throw internal_1.InvalidArgumentError.format("seq", "must be a non-negative integer");
        }
        const subj = `${this.prefix}.CONSUMER.RESET.${stream}.${name}`;
        const body = typeof seq === "number" ? { seq } : void 0;
        const r = await this._request(subj, body);
        return r;
      }
    };
    exports.ConsumerAPIImpl = ConsumerAPIImpl;
    function isPriorityGroup(config) {
      const pg = config;
      return pg && pg.priority_groups !== void 0 || pg.priority_policy !== void 0;
    }
    function validatePriorityGroups(pg) {
      if (isPriorityGroup(pg)) {
        if (!Array.isArray(pg.priority_groups)) {
          throw internal_1.InvalidArgumentError.format(["priority_groups"], "must be an array");
        }
        if (pg.priority_groups.length === 0) {
          throw internal_1.InvalidArgumentError.format(["priority_groups"], "must have at least one group");
        }
        pg.priority_groups.forEach((g) => {
          (0, jsutil_1.minValidation)("priority_group", g);
          if (g.length > 16) {
            throw internal_1.errors.InvalidArgumentError.format("group", "must be 16 characters or less");
          }
        });
        if (pg.priority_policy !== jsapi_types_1.PriorityPolicy.None && pg.priority_policy !== jsapi_types_1.PriorityPolicy.Overflow && pg.priority_policy !== jsapi_types_1.PriorityPolicy.PinnedClient && pg.priority_policy !== jsapi_types_1.PriorityPolicy.Prioritized) {
          throw internal_1.InvalidArgumentError.format(["priority_policy"], "must be 'none', 'prioritized', 'overflow', or 'pinned_client'");
        }
      }
    }
  }
});

// node_modules/@nats-io/jetstream/lib/jsmsg.js
var require_jsmsg = __commonJS({
  "node_modules/@nats-io/jetstream/lib/jsmsg.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.JsMsgImpl = exports.ACK = void 0;
    exports.toJsMsg = toJsMsg;
    exports.parseInfo = parseInfo;
    var internal_1 = require_internal_mod();
    exports.ACK = Uint8Array.of(43, 65, 67, 75);
    var NAK = Uint8Array.of(45, 78, 65, 75);
    var WPI = Uint8Array.of(43, 87, 80, 73);
    var NXT = Uint8Array.of(43, 78, 88, 84);
    var TERM = Uint8Array.of(43, 84, 69, 82, 77);
    var SPACE = Uint8Array.of(32);
    function toJsMsg(m, ackTimeout = 5e3) {
      return new JsMsgImpl(m, ackTimeout);
    }
    function parseInfo(s) {
      const tokens = s.split(".");
      if (tokens.length === 9) {
        tokens.splice(2, 0, "_", "");
      }
      if (tokens.length < 11 || tokens[0] !== "$JS" || tokens[1] !== "ACK") {
        throw new Error(`unable to parse delivery info - not a jetstream message`);
      }
      const di = {};
      di.domain = tokens[2] === "_" ? "" : tokens[2];
      di.account_hash = tokens[3];
      di.stream = tokens[4];
      di.consumer = tokens[5];
      di.deliveryCount = parseInt(tokens[6], 10);
      di.redelivered = di.deliveryCount > 1;
      di.streamSequence = parseInt(tokens[7], 10);
      di.deliverySequence = parseInt(tokens[8], 10);
      di.timestampNanos = parseInt(tokens[9], 10);
      di.pending = parseInt(tokens[10], 10);
      return di;
    }
    function parseTimestampNanos(s) {
      const tokens = s.split(".");
      if (tokens.length === 9) {
        tokens.splice(2, 0, "_", "");
      }
      if (tokens.length < 11 || tokens[0] !== "$JS" || tokens[1] !== "ACK") {
        throw new Error(`unable to parse delivery info - not a jetstream message`);
      }
      return BigInt(tokens[9]);
    }
    var JsMsgImpl = class {
      msg;
      di;
      didAck;
      timeout;
      constructor(msg, timeout) {
        this.msg = msg;
        this.didAck = false;
        this.timeout = timeout;
      }
      get subject() {
        return this.msg.subject;
      }
      get sid() {
        return this.msg.sid;
      }
      get data() {
        return this.msg.data;
      }
      get headers() {
        return this.msg.headers;
      }
      get info() {
        if (!this.di) {
          this.di = parseInfo(this.reply);
        }
        return this.di;
      }
      get redelivered() {
        return this.info.deliveryCount > 1;
      }
      get reply() {
        return this.msg.reply || "";
      }
      get seq() {
        return this.info.streamSequence;
      }
      get time() {
        const ms = (0, internal_1.millis)(this.info.timestampNanos);
        return new Date(ms);
      }
      get timestamp() {
        return this.time.toISOString();
      }
      get timestampNanos() {
        return parseTimestampNanos(this.reply);
      }
      doAck(payload) {
        if (!this.didAck) {
          this.didAck = !this.isWIP(payload);
          this.msg.respond(payload);
        }
      }
      isWIP(p) {
        return p.length === 4 && p[0] === WPI[0] && p[1] === WPI[1] && p[2] === WPI[2] && p[3] === WPI[3];
      }
      // this has to dig into the internals as the message has access
      // to the protocol but not the high-level client.
      async ackAck(opts) {
        const d = (0, internal_1.deferred)();
        if (!this.didAck) {
          this.didAck = true;
          if (this.msg.reply) {
            opts = opts || {};
            opts.timeout = opts.timeout || this.timeout;
            const mi = this.msg;
            const proto = mi.publisher;
            const trace = !(proto.options?.noAsyncTraces || false);
            const r = new internal_1.RequestOne(proto.muxSubscriptions, this.msg.reply, {
              timeout: opts.timeout
            }, trace);
            proto.request(r);
            try {
              proto.publish(this.msg.reply, exports.ACK, {
                reply: `${proto.muxSubscriptions.baseInbox}${r.token}`
              });
            } catch (err) {
              r.cancel(err);
            }
            try {
              await Promise.race([r.timer, r.deferred]);
              d.resolve(true);
            } catch (err) {
              r.cancel(err);
              d.reject(err);
            }
          } else {
            d.resolve(false);
          }
        } else {
          d.resolve(false);
        }
        return d;
      }
      ack() {
        this.doAck(exports.ACK);
      }
      nak(millis) {
        let payload = NAK;
        if (millis) {
          payload = new TextEncoder().encode(`-NAK ${JSON.stringify({ delay: (0, internal_1.nanos)(millis) })}`);
        }
        this.doAck(payload);
      }
      working() {
        this.doAck(WPI);
      }
      next(subj, opts = { batch: 1 }) {
        const args = {};
        args.batch = opts.batch || 1;
        args.no_wait = opts.no_wait || false;
        if (opts.expires && opts.expires > 0) {
          args.expires = (0, internal_1.nanos)(opts.expires);
        }
        const data = new TextEncoder().encode(JSON.stringify(args));
        const payload = internal_1.DataBuffer.concat(NXT, SPACE, data);
        const reqOpts = subj ? { reply: subj } : void 0;
        this.msg.respond(payload, reqOpts);
      }
      term(reason = "") {
        let term = TERM;
        if (reason?.length > 0) {
          term = new TextEncoder().encode(`+TERM ${reason}`);
        }
        this.doAck(term);
      }
      json() {
        return this.msg.json();
      }
      string() {
        return this.msg.string();
      }
    };
    exports.JsMsgImpl = JsMsgImpl;
  }
});

// node_modules/@nats-io/jetstream/lib/consumer.js
var require_consumer = __commonJS({
  "node_modules/@nats-io/jetstream/lib/consumer.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.PullConsumerImpl = exports.PullConsumerMessagesImpl = exports.PullConsumerType = void 0;
    exports.isOverflowOptions = isOverflowOptions;
    exports.isPrioritizedOptions = isPrioritizedOptions;
    exports.validateOverflowPullOptions = validateOverflowPullOptions;
    exports.validatePrioritizedPullOptions = validatePrioritizedPullOptions;
    var internal_1 = require_internal_mod();
    var jsmsg_1 = require_jsmsg();
    var jsapi_types_1 = require_jsapi_types();
    var jserrors_1 = require_jserrors();
    var jsutil_1 = require_jsutil();
    exports.PullConsumerType = {
      Unset: "",
      Consume: "consume",
      Fetch: "fetch"
    };
    function isOverflowOptions(opts) {
      const oo = opts;
      return oo && typeof oo.group === "string" || typeof oo.min_pending === "number" || typeof oo.min_ack_pending === "number";
    }
    function isPrioritizedOptions(opts) {
      const oo = opts;
      return oo && typeof oo.group === "string" && typeof oo.priority === "number";
    }
    var PullConsumerMessagesImpl = class extends internal_1.QueuedIteratorImpl {
      consumer;
      opts;
      sub;
      monitor;
      pending;
      isConsume;
      callback;
      listeners;
      statusIterator;
      abortOnMissingResource;
      bind;
      inboxPrefix;
      inbox;
      cancelables;
      inReset;
      closeListener;
      isPinned;
      isPriority;
      natsPinId;
      // callback: ConsumerCallbackFn;
      constructor(c, opts, refilling = false) {
        super();
        this.consumer = c;
        this.isConsume = refilling;
        this.cancelables = [];
        this.inboxPrefix = (0, internal_1.createInbox)(this.consumer.api.nc.options.inboxPrefix);
        this.inbox = `${this.inboxPrefix}.${this.consumer.serial}`;
        this.inReset = false;
        this.isPinned = false;
        this.isPriority = false;
        this.natsPinId = "";
        if (this.consumer.ordered) {
          if (isOverflowOptions(opts)) {
            throw internal_1.errors.InvalidArgumentError.format([
              "group",
              "min_pending",
              "min_ack_pending"
            ], "cannot be specified for ordered consumers");
          }
          if (this.consumer.orderedConsumerState === void 0) {
            const ocs = {};
            const iopts = c.opts;
            ocs.namePrefix = iopts.name_prefix ?? `oc_${internal_1.nuid.next()}`;
            ocs.opts = iopts;
            ocs.cursor = { stream_seq: 1, deliver_seq: 0 };
            const startSeq = c._info.config.opt_start_seq || 0;
            ocs.cursor.stream_seq = startSeq > 0 ? startSeq - 1 : 0;
            ocs.createFails = 0;
            this.consumer.orderedConsumerState = ocs;
          }
        }
        const copts = opts;
        this.opts = this.parseOptions(opts, this.isConsume);
        this.callback = copts.callback || null;
        this.noIterator = typeof this.callback === "function";
        this.monitor = null;
        this.pending = { msgs: 0, bytes: 0, requests: 0 };
        this.listeners = [];
        this.abortOnMissingResource = copts.abort_on_missing_resource === true;
        this.bind = copts.bind === true;
        if (copts.group) {
          const { min_pending, min_ack_pending } = copts;
          this.isPinned = min_pending === void 0 && min_ack_pending === void 0;
          const { priority } = copts;
          this.isPriority = typeof priority === "number";
        }
        this.closeListener = {
          // we don't propagate the error here
          connectionClosedCallback: () => {
            this._push(() => {
              this.stop();
            });
          }
        };
        this.consumer.api.nc.addCloseListener(this.closeListener);
        this.start();
      }
      start() {
        const { max_messages, max_bytes, idle_heartbeat, threshold_bytes, threshold_messages } = this.opts;
        this.sub = this.consumer.api.nc.subscribe(this.inbox, {
          callback: (err, msg) => {
            if (err) {
              this.stop(err);
              return;
            }
            this.monitor?.work();
            const isProtocol = this.consumer.ordered ? msg.subject.indexOf(this?.inboxPrefix) === 0 : msg.subject === this.inbox;
            if (isProtocol) {
              if (msg.subject !== this.sub.getSubject()) {
                return;
              }
              const status = new jserrors_1.JetStreamStatus(msg);
              const hb = status.parseHeartbeat();
              if (hb) {
                this.notify(hb);
                return;
              }
              const code = status.code;
              const description = status.description;
              const { msgsLeft, bytesLeft } = status.parseDiscard();
              if (msgsLeft && msgsLeft > 0 || bytesLeft && bytesLeft > 0) {
                this.pending.msgs -= msgsLeft;
                this.pending.bytes -= bytesLeft;
                this.pending.requests--;
                this.notify({
                  type: "discard",
                  messagesLeft: msgsLeft,
                  bytesLeft
                });
              }
              switch (code) {
                case 400:
                  this.stop(status.toError());
                  return;
                case 409: {
                  const err2 = this.handle409(status);
                  if (err2) {
                    this.stop(err2);
                    return;
                  }
                  if (status.isMessageSizeExceedsMaxBytes() && this.yields.length > 0) {
                    break;
                  }
                  return;
                }
                case 423:
                  this.natsPinId = "";
                  this.notify({ type: "consumer_unpinned" });
                  break;
                case 503:
                  this.notify({ type: "no_responders", code });
                  if (this.consumer.ordered) {
                    const ocs = this.consumer.orderedConsumerState;
                    ocs.needsReset = true;
                  }
                  if (!this.isConsume) {
                    this.stop(status.toError());
                    return;
                  }
                  break;
                default:
                  this.notify({ type: "debug", code, description });
              }
            } else {
              const m = (0, jsmsg_1.toJsMsg)(msg, this.consumer.api.timeout);
              if (this.isPinned) {
                const pinID = m?.headers?.get("Nats-Pin-Id");
                if (pinID && this.natsPinId === "") {
                  this.natsPinId = pinID;
                  this.notify({ type: "consumer_pinned", id: pinID });
                }
              }
              if (this.consumer.ordered) {
                const cursor = this.consumer.orderedConsumerState.cursor;
                const dseq = m.info.deliverySequence;
                const sseq = m.info.streamSequence;
                const expected_dseq = cursor.deliver_seq + 1;
                if (dseq !== expected_dseq) {
                  this.reset();
                  return;
                }
                cursor.deliver_seq = dseq;
                cursor.stream_seq = sseq;
              }
              this._push(m);
              this.received++;
              if (this.pending.msgs) {
                this.pending.msgs--;
              }
              if (this.pending.bytes) {
                this.pending.bytes -= msg.size();
              }
            }
            if (this.pending.msgs === 0 && this.pending.bytes === 0) {
              this.pending.requests = 0;
            }
            if (this.isConsume) {
              if (max_messages && this.pending.msgs <= threshold_messages || max_bytes && this.pending.bytes <= threshold_bytes) {
                const batch = this.pullOptions();
                this.pull(batch);
              }
            } else if (this.pending.requests === 0) {
              this._push(() => {
                this.stop();
              });
            }
          }
        });
        if (idle_heartbeat) {
          this.monitor = new internal_1.IdleHeartbeatMonitor(idle_heartbeat, (count) => {
            this.notify({ type: "heartbeats_missed", count });
            if (!this.isConsume && !this.consumer.ordered) {
              this.stop(new jserrors_1.JetStreamError("heartbeats missed"));
              return true;
            }
            this.resetPending().then(() => {
            }).catch(() => {
            });
            return false;
          }, { maxOut: 2 });
        }
        (async () => {
          const status = this.consumer.api.nc.status();
          this.statusIterator = status;
          for await (const s of status) {
            switch (s.type) {
              case "disconnect":
                this.monitor?.cancel();
                break;
              case "reconnect":
                this.resetPending().then((ok) => {
                  if (ok) {
                    this.monitor?.restart();
                  }
                }).catch(() => {
                });
                break;
              default:
            }
          }
        })();
        this.sub.closed.then(() => {
          if (this.sub.isDraining()) {
            this._push(() => {
              this.stop();
            });
          }
        });
        this.pull(this.pullOptions());
      }
      /**
       * Handle the notification of 409 error and whether
       * it should reject the operation by returning an Error or null
       * @param status
       */
      handle409(status) {
        const { code, description } = status;
        if (status.isConsumerDeleted()) {
          this.notify({ type: "consumer_deleted", code, description });
        } else if (status.isExceededLimit()) {
          this.notify({ type: "exceeded_limits", code, description });
        }
        if (!this.isConsume) {
          return status.toError();
        }
        if (status.isConsumerDeleted() && this.abortOnMissingResource) {
          return status.toError();
        }
        return null;
      }
      reset() {
        this.monitor?.cancel();
        const ocs = this.consumer.orderedConsumerState;
        const { name } = this.consumer._info?.config;
        if (name) {
          this.notify({ type: "reset", name });
          this.consumer.api.delete(this.consumer.stream, name).catch(() => {
          });
        }
        const config = this.consumer.getConsumerOpts();
        this.inbox = `${this.inboxPrefix}.${this.consumer.serial}`;
        ocs.cursor.deliver_seq = 0;
        this.consumer.name = config.name;
        this.consumer.api.nc._resub(this.sub, this.inbox);
        this.consumer.api.add(this.consumer.stream, config).then((ci) => {
          ocs.createFails = 0;
          this.consumer._info = ci;
          this.notify({ type: "ordered_consumer_recreated", name: ci.name });
          this.monitor?.restart();
          this.pull(this.pullOptions());
        }).catch((err) => {
          ocs.createFails++;
          if (err.message === "stream not found") {
            this.notify({
              type: "stream_not_found",
              consumerCreateFails: ocs.createFails,
              name: this.consumer.stream
            });
            if (this.abortOnMissingResource) {
              this.stop(err);
              return;
            }
          }
          if (ocs.createFails >= 30 && this.received === 0) {
            this.stop(err);
          }
          const bo = (0, internal_1.backoff)();
          const c = (0, internal_1.delay)(bo.backoff(ocs.createFails));
          c.then(() => {
            const idx = this.cancelables.indexOf(c);
            if (idx !== -1) {
              this.cancelables = this.cancelables.splice(idx, idx);
            }
            if (!this.done) {
              this.reset();
            }
          }).catch((_) => {
          });
          this.cancelables.push(c);
        });
      }
      _push(r) {
        if (!this.callback) {
          super.push(r);
        } else {
          const fn = typeof r === "function" ? r : null;
          try {
            if (!fn) {
              const m = r;
              this.callback(m);
            } else {
              fn();
            }
          } catch (err) {
            this.stop(err);
          }
        }
      }
      notify(n) {
        if (this.listeners.length > 0) {
          (() => {
            this.listeners.forEach((l) => {
              const qi = l;
              if (!qi.done) {
                qi.push(n);
              }
            });
          })();
        }
      }
      async resetPending() {
        if (this.inReset) {
          return Promise.resolve(true);
        }
        this.inReset = true;
        const v = this.bind ? this.resetPendingNoInfo() : this.resetPendingWithInfo();
        const tf = await v;
        this.inReset = false;
        return tf;
      }
      resetPendingNoInfo() {
        this.pending.msgs = 0;
        this.pending.bytes = 0;
        this.pending.requests = 0;
        this.pull(this.pullOptions());
        return Promise.resolve(true);
      }
      async resetPendingWithInfo() {
        let notFound = 0;
        let streamNotFound = 0;
        const bo = (0, internal_1.backoff)([this.opts.expires || 3e4]);
        let attempt = 0;
        while (true) {
          if (this.done) {
            return false;
          }
          if (this.consumer.api.nc.isClosed()) {
            return false;
          }
          try {
            await this.consumer.info();
            notFound = 0;
            this.pending.msgs = 0;
            this.pending.bytes = 0;
            this.pending.requests = 0;
            this.pull(this.pullOptions());
            return true;
          } catch (err) {
            if (err instanceof internal_1.errors.ClosedConnectionError) {
              this.stop(err);
              return false;
            }
            if (err.message === "stream not found") {
              streamNotFound++;
              this.notify({ type: "stream_not_found", name: this.consumer.stream });
              if (!this.isConsume || this.abortOnMissingResource) {
                this.stop(err);
                return false;
              }
            } else if (err.message === "consumer not found") {
              notFound++;
              this.notify({
                type: "consumer_not_found",
                name: this.consumer.name,
                stream: this.consumer.stream,
                count: notFound
              });
              if (!this.isConsume || this.abortOnMissingResource) {
                if (this.consumer.ordered) {
                  const ocs = this.consumer.orderedConsumerState;
                  ocs.needsReset = true;
                }
                this.stop(err);
                return false;
              }
              if (this.consumer.ordered) {
                this.reset();
                return false;
              }
            } else {
              notFound = 0;
              streamNotFound = 0;
            }
            const to = bo.backoff(attempt);
            const de = (0, internal_1.delay)(to);
            await Promise.race([de, this.consumer.api.nc.closed()]);
            de.cancel();
            attempt++;
          }
        }
      }
      pull(opts) {
        this.pending.bytes += opts.max_bytes ?? 0;
        this.pending.msgs += opts.batch ?? 0;
        this.pending.requests++;
        const nc = this.consumer.api.nc;
        const subj = `${this.consumer.api.prefix}.CONSUMER.MSG.NEXT.${this.consumer.stream}.${this.consumer._info.name}`;
        this._push(() => {
          nc.publish(subj, JSON.stringify(opts), { reply: this.inbox });
          this.notify({ type: "next", options: opts });
        });
      }
      pullOptions() {
        const batch = this.opts.max_messages - this.pending.msgs;
        const max_bytes = this.opts.max_bytes - this.pending.bytes;
        const idle_heartbeat = (0, internal_1.nanos)(this.opts.idle_heartbeat);
        const expires = (0, internal_1.nanos)(this.opts.expires);
        const opts = { batch, max_bytes, idle_heartbeat, expires };
        if (this.isPinned && this.natsPinId !== "") {
          opts.id = this.natsPinId;
        }
        if (isOverflowOptions(this.opts)) {
          opts.group = this.opts.group;
          if (this.opts.min_pending) {
            opts.min_pending = this.opts.min_pending;
          }
          if (this.opts.min_ack_pending) {
            opts.min_ack_pending = this.opts.min_ack_pending;
          }
        }
        if (isPrioritizedOptions(this.opts)) {
          opts.group = this.opts.group;
          opts.priority = this.opts.priority;
        }
        return opts;
      }
      close() {
        this.stop();
        return this.iterClosed;
      }
      closed() {
        return this.iterClosed;
      }
      clearTimers() {
        this.monitor?.cancel();
        this.monitor = null;
      }
      stop(err) {
        if (this.done) {
          return;
        }
        this.consumer.api.nc.removeCloseListener(this.closeListener);
        this.sub?.unsubscribe();
        this.clearTimers();
        this.statusIterator?.stop();
        this._push(() => {
          super.stop(err);
          this.listeners.forEach((iter) => {
            iter.stop();
          });
        });
      }
      parseOptions(opts, refilling = false) {
        const args = opts || {};
        args.max_messages = args.max_messages || 0;
        args.max_bytes = args.max_bytes || 0;
        if (args.max_messages !== 0 && args.max_bytes !== 0) {
          throw internal_1.errors.InvalidArgumentError.format(["max_messages", "max_bytes"], "are mutually exclusive");
        }
        if (args.max_messages === 0) {
          args.max_messages = 100;
        }
        args.expires = args.expires || 3e4;
        if (args.expires < 1e3) {
          throw internal_1.errors.InvalidArgumentError.format("expires", "must be at least 1000ms");
        }
        args.idle_heartbeat = args.idle_heartbeat || args.expires / 2;
        args.idle_heartbeat = args.idle_heartbeat > 3e4 ? 3e4 : args.idle_heartbeat;
        if (args.idle_heartbeat < 500) {
          args.idle_heartbeat = 500;
        }
        if (refilling) {
          const minMsgs = Math.round(args.max_messages * 0.75) || 1;
          args.threshold_messages = args.threshold_messages || minMsgs;
          const minBytes = Math.round(args.max_bytes * 0.75) || 1;
          args.threshold_bytes = args.threshold_bytes || minBytes;
        }
        if (isOverflowOptions(opts)) {
          const { min, ok } = this.consumer.api.nc.features.get(internal_1.Feature.JS_PRIORITY_GROUPS);
          if (!ok) {
            throw new Error(`priority_groups require server ${min}`);
          }
          validateOverflowPullOptions(opts);
          if (opts.group) {
            args.group = opts.group;
          }
          if (opts.min_ack_pending) {
            args.min_ack_pending = opts.min_ack_pending;
          }
          if (opts.min_pending) {
            args.min_pending = opts.min_pending;
          }
        } else if (isPrioritizedOptions(opts)) {
          validatePrioritizedPullOptions(opts);
          if (opts.group) {
            args.group = opts.group;
          }
          if (typeof opts.priority === "number") {
            args.priority = opts.priority;
          }
        }
        return args;
      }
      status() {
        const iter = new internal_1.QueuedIteratorImpl();
        this.listeners.push(iter);
        return iter;
      }
    };
    exports.PullConsumerMessagesImpl = PullConsumerMessagesImpl;
    var PullConsumerImpl = class {
      api;
      _info;
      stream;
      name;
      opts;
      type;
      messages;
      ordered;
      serial;
      orderedConsumerState;
      constructor(api, info, opts = null) {
        this.api = api;
        this._info = info;
        this.name = info.name;
        this.stream = info.stream_name;
        this.ordered = opts !== null;
        this.opts = opts || {};
        this.serial = 1;
        this.type = exports.PullConsumerType.Unset;
      }
      debug() {
        console.log({
          serial: this.serial,
          cursor: this.orderedConsumerState?.cursor
        });
      }
      isPullConsumer() {
        return true;
      }
      isPushConsumer() {
        return false;
      }
      consume(opts = {
        max_messages: 100,
        expires: 3e4
      }) {
        opts = { ...opts };
        if (this.ordered) {
          if (opts.bind) {
            return Promise.reject(internal_1.errors.InvalidArgumentError.format("bind", "is not supported"));
          }
          if (this.type === exports.PullConsumerType.Fetch) {
            return Promise.reject(new internal_1.errors.InvalidOperationError("ordered consumer initialized as fetch"));
          }
          if (this.type === exports.PullConsumerType.Consume) {
            return Promise.reject(new internal_1.errors.InvalidOperationError("ordered consumer doesn't support concurrent consume"));
          }
          this.type = exports.PullConsumerType.Consume;
        }
        return Promise.resolve(new PullConsumerMessagesImpl(this, opts, true));
      }
      async fetch(opts = {
        max_messages: 100,
        expires: 3e4
      }) {
        opts = { ...opts };
        if (this.ordered) {
          if (opts.group) {
            return Promise.reject(internal_1.errors.InvalidArgumentError.format("group", "ordered consumers don't support priority groups"));
          }
          if (opts.bind) {
            return Promise.reject(internal_1.errors.InvalidArgumentError.format("bind", "is not supported"));
          }
          if (this.type === exports.PullConsumerType.Consume) {
            return Promise.reject(new internal_1.errors.InvalidOperationError("ordered consumer already initialized as consume"));
          }
          if (this.messages?.done === false) {
            return Promise.reject(new internal_1.errors.InvalidOperationError("ordered consumer doesn't support concurrent fetch"));
          }
          if (this.ordered) {
            if (this.orderedConsumerState?.cursor?.deliver_seq) {
              this._info.config.opt_start_seq = this.orderedConsumerState?.cursor.stream_seq + 1;
            }
            if (this.orderedConsumerState?.needsReset === true) {
              await this._reset();
            }
          }
          this.type = exports.PullConsumerType.Fetch;
        }
        const m = new PullConsumerMessagesImpl(this, opts);
        if (this.ordered) {
          this.messages = m;
        }
        return Promise.resolve(m);
      }
      async next(opts = { expires: 3e4 }) {
        opts = { ...opts };
        const fopts = opts;
        fopts.max_messages = 1;
        const iter = await this.fetch(fopts);
        try {
          for await (const m of iter) {
            return m;
          }
        } catch (err) {
          return Promise.reject(err);
        }
        return null;
      }
      delete() {
        const { stream_name, name } = this._info;
        return this.api.delete(stream_name, name);
      }
      getConsumerOpts() {
        const ocs = this.orderedConsumerState;
        this.serial++;
        this.name = `${ocs.namePrefix}_${this.serial}`;
        const conf = Object.assign({}, this._info.config, {
          name: this.name,
          deliver_policy: jsapi_types_1.DeliverPolicy.StartSequence,
          opt_start_seq: ocs.cursor.stream_seq + 1,
          ack_policy: jsapi_types_1.AckPolicy.None,
          inactive_threshold: (0, internal_1.nanos)(5 * 60 * 1e3),
          num_replicas: 1
        });
        delete conf.metadata;
        return conf;
      }
      async _reset() {
        if (this.messages === void 0) {
          throw new Error("not possible to reset");
        }
        this.delete().catch(() => {
        });
        const conf = this.getConsumerOpts();
        const ci = await this.api.add(this.stream, conf);
        this._info = ci;
        return ci;
      }
      async info(cached = false) {
        if (cached) {
          return Promise.resolve(this._info);
        }
        const { stream_name, name } = this._info;
        this._info = await this.api.info(stream_name, name);
        return this._info;
      }
    };
    exports.PullConsumerImpl = PullConsumerImpl;
    function validateOverflowPullOptions(opts) {
      if (isOverflowOptions(opts)) {
        (0, jsutil_1.minValidation)("group", opts.group);
        if (opts.group.length > 16) {
          throw internal_1.errors.InvalidArgumentError.format("group", "must be 16 characters or less");
        }
        const { min_pending, min_ack_pending } = opts;
        if (min_pending && typeof min_pending !== "number") {
          throw internal_1.errors.InvalidArgumentError.format(["min_pending"], "must be a number");
        }
        if (min_ack_pending && typeof min_ack_pending !== "number") {
          throw internal_1.errors.InvalidArgumentError.format(["min_ack_pending"], "must be a number");
        }
      }
    }
    function validatePrioritizedPullOptions(opts) {
      if (isPrioritizedOptions(opts)) {
        (0, jsutil_1.minValidation)("group", opts.group);
        if (opts.group.length > 16) {
          throw internal_1.errors.InvalidArgumentError.format("group", "must be 16 characters or less");
        }
        const { priority } = opts;
        if (priority && typeof priority !== "number") {
          throw internal_1.errors.InvalidArgumentError.format(["priority"], "must be a number");
        }
      }
    }
  }
});

// node_modules/@nats-io/jetstream/lib/pushconsumer.js
var require_pushconsumer = __commonJS({
  "node_modules/@nats-io/jetstream/lib/pushconsumer.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.PushConsumerImpl = exports.PushConsumerMessagesImpl = void 0;
    var jsmsg_1 = require_jsmsg();
    var jsapi_types_1 = require_jsapi_types();
    var types_1 = require_types2();
    var internal_1 = require_internal_mod();
    var jserrors_1 = require_jserrors();
    var PushConsumerMessagesImpl = class extends internal_1.QueuedIteratorImpl {
      consumer;
      sub;
      monitor;
      listeners;
      abortOnMissingResource;
      callback;
      ordered;
      cursor;
      namePrefix;
      deliverPrefix;
      serial;
      createFails;
      statusIterator;
      cancelables;
      constructor(c, userOptions = {}, internalOptions = {}) {
        super();
        this.consumer = c;
        this.monitor = null;
        this.listeners = [];
        this.cancelables = [];
        this.abortOnMissingResource = userOptions.abort_on_missing_resource === true;
        this.callback = userOptions.callback || null;
        this.noIterator = this.callback !== null;
        this.namePrefix = null;
        this.deliverPrefix = null;
        this.ordered = internalOptions.ordered === true;
        this.serial = 1;
        if (this.ordered) {
          this.namePrefix = internalOptions.name_prefix ?? `oc_${internal_1.nuid.next()}`;
          this.deliverPrefix = internalOptions.deliver_prefix ?? (0, internal_1.createInbox)(this.consumer.api.nc.options.inboxPrefix);
          this.cursor = { stream_seq: 1, deliver_seq: 0 };
          const startSeq = c._info.config.opt_start_seq || 0;
          this.cursor.stream_seq = startSeq > 0 ? startSeq - 1 : 0;
          this.createFails = 0;
        }
        this.start();
      }
      reset() {
        const { name } = this.consumer._info?.config;
        if (name) {
          this.consumer.api.delete(this.consumer.stream, name).catch(() => {
          });
        }
        const config = this.getConsumerOpts();
        this.cursor.deliver_seq = 0;
        this.consumer.name = config.name;
        this.consumer.serial = this.serial;
        this.consumer.api.nc._resub(this.sub, config.deliver_subject);
        this.consumer.api.add(this.consumer.stream, config).then((ci) => {
          this.createFails = 0;
          this.consumer._info = ci;
          this.notify({ type: "ordered_consumer_recreated", name: ci.name });
        }).catch((err) => {
          this.createFails++;
          if (err.message === "stream not found") {
            this.notify({
              type: "stream_not_found",
              name: this.consumer.stream,
              consumerCreateFails: this.createFails
            });
            if (this.abortOnMissingResource) {
              this.stop(err);
              return;
            }
          }
          if (this.createFails >= 30 && this.received === 0) {
            this.stop(err);
          }
          const bo = (0, internal_1.backoff)();
          const c = (0, internal_1.delay)(bo.backoff(this.createFails));
          c.then(() => {
            if (!this.done) {
              this.reset();
            }
          }).catch(() => {
          }).finally(() => {
            const idx = this.cancelables.indexOf(c);
            if (idx !== -1) {
              this.cancelables = this.cancelables.splice(idx, idx);
            }
          });
          this.cancelables.push(c);
        });
      }
      getConsumerOpts() {
        const src = Object.assign({}, this.consumer._info.config);
        this.serial++;
        const name = `${this.namePrefix}_${this.serial}`;
        return Object.assign(src, {
          name,
          deliver_policy: jsapi_types_1.DeliverPolicy.StartSequence,
          opt_start_seq: this.cursor.stream_seq + 1,
          ack_policy: jsapi_types_1.AckPolicy.None,
          inactive_threshold: (0, internal_1.nanos)(5 * 60 * 1e3),
          num_replicas: 1,
          flow_control: true,
          idle_heartbeat: (0, internal_1.nanos)(30 * 1e3),
          deliver_subject: `${this.deliverPrefix}.${this.serial}`
        });
      }
      closed() {
        return this.iterClosed;
      }
      close() {
        this.stop();
        return this.iterClosed;
      }
      stop(err) {
        if (this.done) {
          return;
        }
        this.statusIterator?.stop();
        this.monitor?.cancel();
        this.monitor = null;
        this.cancelables.forEach((c) => {
          c.cancel();
        });
        Promise.all(this.cancelables).then(() => {
          this.cancelables = [];
        }).catch(() => {
        }).finally(() => {
          this._push(() => {
            super.stop(err);
            this.listeners.forEach((n) => {
              n.stop();
            });
          });
        });
      }
      _push(r) {
        if (!this.callback) {
          super.push(r);
        } else {
          const fn = typeof r === "function" ? r : null;
          try {
            if (!fn) {
              const m = r;
              this.received++;
              this.callback(m);
              this.processed++;
            } else {
              fn();
            }
          } catch (err) {
            this.stop(err);
          }
        }
      }
      status() {
        const iter = new internal_1.QueuedIteratorImpl();
        this.listeners.push(iter);
        return iter;
      }
      start() {
        const { deliver_subject: subject, deliver_group: queue, idle_heartbeat: hbNanos } = this.consumer._info.config;
        if (!subject) {
          throw new Error("bad consumer info");
        }
        if (hbNanos) {
          const ms = (0, internal_1.millis)(hbNanos);
          this.monitor = new internal_1.IdleHeartbeatMonitor(ms, (count) => {
            this.notify({ type: "heartbeats_missed", count });
            if (this.ordered) {
              this.reset();
            }
            return false;
          }, { maxOut: 2 });
          (async () => {
            this.statusIterator = this.consumer.api.nc.status();
            for await (const s of this.statusIterator) {
              switch (s.type) {
                case "disconnect":
                  this.monitor?.cancel();
                  break;
                case "reconnect":
                  this.monitor?.restart();
                  break;
                default:
              }
            }
          })();
        }
        this.sub = this.consumer.api.nc.subscribe(subject, {
          queue,
          callback: (err, msg) => {
            if (err) {
              this.stop(err);
              return;
            }
            this.monitor?.work();
            const isProtocol = this.ordered ? msg.subject.indexOf(this?.deliverPrefix) === 0 : msg.subject === subject;
            if (isProtocol) {
              if (msg.subject !== this.sub.getSubject()) {
                return;
              }
              const status = new jserrors_1.JetStreamStatus(msg);
              if (status.isFlowControlRequest()) {
                this._push(() => {
                  msg.respond();
                  this.notify({ type: "flow_control" });
                });
                return;
              }
              if (status.isIdleHeartbeat()) {
                const lastConsumerSequence = parseInt(msg.headers?.get(types_1.JsHeaders.LastConsumerSeqHdr) || "0");
                const lastStreamSequence = parseInt(msg.headers?.get(types_1.JsHeaders.LastStreamSeqHdr) ?? "0");
                this.notify({
                  type: "heartbeat",
                  lastStreamSequence,
                  lastConsumerSequence
                });
                const maybeStuck = msg.headers?.get(types_1.JsHeaders.ConsumerStalledHdr);
                if (typeof maybeStuck === "string" && maybeStuck !== "") {
                  msg.publisher.publish(maybeStuck, internal_1.Empty);
                }
                return;
              }
              const code = status.code;
              const description = status.description;
              if (status.isConsumerDeleted()) {
                this.notify({ type: "consumer_deleted", code, description });
              }
              if (this.abortOnMissingResource) {
                this._push(() => {
                  this.stop(status.toError());
                });
                return;
              }
            } else {
              const m = (0, jsmsg_1.toJsMsg)(msg);
              if (this.ordered) {
                const dseq = m.info.deliverySequence;
                if (dseq !== this.cursor.deliver_seq + 1) {
                  this.reset();
                  return;
                }
                this.cursor.deliver_seq = dseq;
                this.cursor.stream_seq = m.info.streamSequence;
              }
              this._push(m);
            }
          }
        });
        this.sub.closed.then(() => {
          this._push(() => {
            this.stop();
          });
        });
        this.closed().then(() => {
          this.sub?.unsubscribe();
        });
      }
      notify(n) {
        if (this.listeners.length > 0) {
          (() => {
            this.listeners.forEach((l) => {
              const qi = l;
              if (!qi.done) {
                qi.push(n);
              }
            });
          })();
        }
      }
    };
    exports.PushConsumerMessagesImpl = PushConsumerMessagesImpl;
    var PushConsumerImpl = class {
      api;
      _info;
      stream;
      name;
      bound;
      ordered;
      started;
      serial;
      opts;
      constructor(api, info, opts = {}) {
        this.api = api;
        this._info = info;
        this.stream = info.stream_name;
        this.name = info.name;
        this.bound = opts.bound === true;
        this.started = false;
        this.opts = opts;
        this.serial = 0;
        this.ordered = opts.ordered || false;
        if (this.ordered) {
          this.serial = 1;
        }
      }
      consume(userOptions = {}) {
        userOptions = { ...userOptions };
        if (this.started) {
          return Promise.reject(new internal_1.errors.InvalidOperationError("consumer already started"));
        }
        if (!this._info.config.deliver_subject) {
          return Promise.reject(new Error("deliver_subject is not set, not a push consumer"));
        }
        if (!this._info.config.deliver_group && this._info.push_bound) {
          return Promise.reject(new internal_1.errors.InvalidOperationError("consumer is already bound"));
        }
        const v = new PushConsumerMessagesImpl(this, userOptions, this.opts);
        this.started = true;
        v.closed().then(() => {
          this.started = false;
        });
        return Promise.resolve(v);
      }
      delete() {
        if (this.bound) {
          return Promise.reject(new internal_1.errors.InvalidOperationError("bound consumers cannot delete"));
        }
        const { stream_name, name } = this._info;
        return this.api.delete(stream_name, name);
      }
      async info(cached) {
        if (this.bound) {
          return Promise.reject(new internal_1.errors.InvalidOperationError("bound consumers cannot info"));
        }
        if (cached) {
          return Promise.resolve(this._info);
        }
        const info = await this.api.info(this.stream, this.name);
        this._info = info;
        return info;
      }
      isPullConsumer() {
        return false;
      }
      isPushConsumer() {
        return true;
      }
    };
    exports.PushConsumerImpl = PushConsumerImpl;
  }
});

// node_modules/@nats-io/jetstream/lib/jsmstream_api.js
var require_jsmstream_api = __commonJS({
  "node_modules/@nats-io/jetstream/lib/jsmstream_api.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.StreamsImpl = exports.StoredMsgImpl = exports.StreamAPIImpl = exports.StreamImpl = exports.ConsumersImpl = void 0;
    exports.convertStreamSourceDomain = convertStreamSourceDomain;
    var internal_1 = require_internal_mod();
    var jsbaseclient_api_1 = require_jsbaseclient_api();
    var jslister_1 = require_jslister();
    var jsutil_1 = require_jsutil();
    var types_1 = require_types2();
    var jsapi_types_1 = require_jsapi_types();
    var consumer_1 = require_consumer();
    var jsmconsumer_api_1 = require_jsmconsumer_api();
    var pushconsumer_1 = require_pushconsumer();
    var jserrors_1 = require_jserrors();
    function convertStreamSourceDomain(s) {
      if (s === void 0) {
        return void 0;
      }
      const { domain } = s;
      if (domain === void 0) {
        return s;
      }
      const copy = Object.assign({}, s);
      delete copy.domain;
      if (domain === "") {
        return copy;
      }
      if (copy.external) {
        throw internal_1.InvalidArgumentError.format(["domain", "external"], "are mutually exclusive");
      }
      copy.external = { api: `$JS.${domain}.API` };
      return copy;
    }
    var ConsumersImpl = class {
      api;
      notified;
      constructor(api) {
        this.api = api;
        this.notified = false;
      }
      checkVersion() {
        const fv = this.api.nc.features.get(internal_1.Feature.JS_SIMPLIFICATION);
        if (!fv.ok) {
          return Promise.reject(new Error(`consumers framework is only supported on servers ${fv.min} or better`));
        }
        return Promise.resolve();
      }
      async getPushConsumer(stream, name) {
        await this.checkVersion();
        (0, jsutil_1.minValidation)("stream", stream);
        if (typeof name === "string") {
          (0, jsutil_1.minValidation)("name", name);
          const ci = await this.api.info(stream, name);
          if (typeof ci.config.deliver_subject !== "string") {
            return Promise.reject(new Error("not a push consumer"));
          }
          return new pushconsumer_1.PushConsumerImpl(this.api, ci);
        } else if (name === void 0) {
          return this.getOrderedPushConsumer(stream);
        } else if ((0, types_1.isOrderedPushConsumerOptions)(name)) {
          const opts = name;
          return this.getOrderedPushConsumer(stream, opts);
        }
        return Promise.reject(new Error("unsupported push consumer type"));
      }
      async getOrderedPushConsumer(stream, opts = {}) {
        opts = Object.assign({}, opts);
        let { name_prefix, deliver_prefix, filter_subjects } = opts;
        delete opts.deliver_prefix;
        delete opts.name_prefix;
        delete opts.filter_subjects;
        if (typeof opts.opt_start_seq === "number") {
          opts.deliver_policy = jsapi_types_1.DeliverPolicy.StartSequence;
        }
        if (typeof opts.opt_start_time === "string") {
          opts.deliver_policy = jsapi_types_1.DeliverPolicy.StartTime;
        }
        name_prefix = name_prefix || `oc_${internal_1.nuid.next()}`;
        (0, jsutil_1.minValidation)("name_prefix", name_prefix);
        deliver_prefix = deliver_prefix || (0, internal_1.createInbox)(this.api.getOptions().watcherPrefix);
        const cc = Object.assign({}, opts);
        cc.ack_policy = jsapi_types_1.AckPolicy.None;
        cc.inactive_threshold = (0, internal_1.nanos)(5 * 60 * 1e3);
        cc.num_replicas = 1;
        cc.max_deliver = 1;
        cc.flow_control = true;
        cc.idle_heartbeat = (0, internal_1.nanos)(3e4);
        if (Array.isArray(filter_subjects)) {
          cc.filter_subjects = filter_subjects;
        }
        if (typeof filter_subjects === "string") {
          cc.filter_subject = filter_subjects;
        }
        if (typeof cc.filter_subjects === "undefined" && typeof cc.filter_subject === "undefined") {
          cc.filter_subject = ">";
        }
        cc.name = `${name_prefix}_1`;
        cc.deliver_subject = `${deliver_prefix}.1`;
        const ci = await this.api.add(stream, cc);
        const iopts = {
          name_prefix,
          deliver_prefix,
          ordered: true
        };
        return new pushconsumer_1.PushConsumerImpl(this.api, ci, iopts);
      }
      getBoundPushConsumer(opts) {
        if ((0, types_1.isBoundPushConsumerOptions)(opts)) {
          const ci = { config: opts };
          return Promise.resolve(new pushconsumer_1.PushConsumerImpl(this.api, ci, { bound: true }));
        } else {
          return Promise.reject(internal_1.errors.InvalidArgumentError.format("deliver_subject", "is required"));
        }
      }
      async get(stream, name) {
        await this.checkVersion();
        if (typeof name === "string") {
          const ci = await this.api.info(stream, name);
          if (typeof ci.config.deliver_subject === "string") {
            return Promise.reject(new Error("not a pull consumer"));
          } else {
            return new consumer_1.PullConsumerImpl(this.api, ci);
          }
        } else {
          return this.ordered(stream, name);
        }
      }
      getConsumerFromInfo(ci) {
        if (typeof ci.config.deliver_subject === "string") {
          throw new Error("not a pull consumer");
        }
        return new consumer_1.PullConsumerImpl(this.api, ci);
      }
      async ordered(stream, opts = {}) {
        await this.checkVersion();
        const impl = this.api;
        const sapi = new StreamAPIImpl(impl.nc, impl.opts);
        await sapi.info(stream);
        if (typeof opts.name_prefix === "string") {
          (0, jsutil_1.minValidation)("name_prefix", opts.name_prefix);
        }
        opts.name_prefix = opts.name_prefix || internal_1.nuid.next();
        const name = `${opts.name_prefix}_1`;
        const config = {
          name,
          deliver_policy: jsapi_types_1.DeliverPolicy.StartSequence,
          opt_start_seq: opts.opt_start_seq || 1,
          ack_policy: jsapi_types_1.AckPolicy.None,
          inactive_threshold: (0, internal_1.nanos)(5 * 60 * 1e3),
          num_replicas: 1,
          max_deliver: 1,
          mem_storage: true
        };
        if (opts.headers_only === true) {
          config.headers_only = true;
        }
        if (Array.isArray(opts.filter_subjects)) {
          config.filter_subjects = opts.filter_subjects;
        }
        if (typeof opts.filter_subjects === "string") {
          config.filter_subject = opts.filter_subjects;
        }
        if (opts.replay_policy) {
          config.replay_policy = opts.replay_policy;
        }
        config.deliver_policy = opts.deliver_policy || jsapi_types_1.DeliverPolicy.StartSequence;
        if (opts.deliver_policy === jsapi_types_1.DeliverPolicy.All || opts.deliver_policy === jsapi_types_1.DeliverPolicy.LastPerSubject || opts.deliver_policy === jsapi_types_1.DeliverPolicy.New || opts.deliver_policy === jsapi_types_1.DeliverPolicy.Last) {
          delete config.opt_start_seq;
          config.deliver_policy = opts.deliver_policy;
        }
        if (config.deliver_policy === jsapi_types_1.DeliverPolicy.LastPerSubject) {
          if (typeof config.filter_subjects === "undefined" && typeof config.filter_subject === "undefined") {
            config.filter_subject = ">";
          }
        }
        if (opts.opt_start_time) {
          delete config.opt_start_seq;
          config.deliver_policy = jsapi_types_1.DeliverPolicy.StartTime;
          config.opt_start_time = opts.opt_start_time;
        }
        if (opts.inactive_threshold) {
          config.inactive_threshold = (0, internal_1.nanos)(opts.inactive_threshold);
        }
        const ci = await this.api.add(stream, config);
        return Promise.resolve(new consumer_1.PullConsumerImpl(this.api, ci, opts));
      }
    };
    exports.ConsumersImpl = ConsumersImpl;
    var StreamImpl = class _StreamImpl {
      api;
      _info;
      constructor(api, info) {
        this.api = api;
        this._info = info;
      }
      get name() {
        return this._info.config.name;
      }
      alternates() {
        return this.info().then((si) => {
          return si.alternates ? si.alternates : [];
        });
      }
      async best() {
        await this.info();
        if (this._info.alternates) {
          const asi = await this.api.info(this._info.alternates[0].name);
          return new _StreamImpl(this.api, asi);
        } else {
          return this;
        }
      }
      info(cached = false, opts) {
        if (cached) {
          return Promise.resolve(this._info);
        }
        return this.api.info(this.name, opts).then((si) => {
          this._info = si;
          return this._info;
        });
      }
      getConsumer(name) {
        return new ConsumersImpl(new jsmconsumer_api_1.ConsumerAPIImpl(this.api.nc, this.api.opts)).get(this.name, name);
      }
      getPushConsumer(name) {
        return new ConsumersImpl(new jsmconsumer_api_1.ConsumerAPIImpl(this.api.nc, this.api.opts)).getPushConsumer(this.name, name);
      }
      getMessage(query) {
        return this.api.getMessage(this.name, query);
      }
      deleteMessage(seq, erase = true) {
        return this.api.deleteMessage(this.name, seq, erase);
      }
      resetConsumer(name, seq) {
        return new jsmconsumer_api_1.ConsumerAPIImpl(this.api.nc, this.api.opts).reset(this.name, name, seq);
      }
    };
    exports.StreamImpl = StreamImpl;
    var StreamAPIImpl = class extends jsbaseclient_api_1.BaseApiClientImpl {
      constructor(nc, opts) {
        super(nc, opts);
      }
      checkStreamConfigVersions(cfg) {
        const nci = this.nc;
        if (cfg.metadata) {
          const { min, ok } = nci.features.get(internal_1.Feature.JS_STREAM_CONSUMER_METADATA);
          if (!ok) {
            throw new Error(`stream 'metadata' requires server ${min}`);
          }
        }
        if (cfg.first_seq) {
          const { min, ok } = nci.features.get(internal_1.Feature.JS_STREAM_FIRST_SEQ);
          if (!ok) {
            throw new Error(`stream 'first_seq' requires server ${min}`);
          }
        }
        if (cfg.subject_transform) {
          const { min, ok } = nci.features.get(internal_1.Feature.JS_STREAM_SUBJECT_TRANSFORM);
          if (!ok) {
            throw new Error(`stream 'subject_transform' requires server ${min}`);
          }
        }
        if (cfg.compression) {
          const { min, ok } = nci.features.get(internal_1.Feature.JS_STREAM_COMPRESSION);
          if (!ok) {
            throw new Error(`stream 'compression' requires server ${min}`);
          }
        }
        if (cfg.consumer_limits) {
          const { min, ok } = nci.features.get(internal_1.Feature.JS_DEFAULT_CONSUMER_LIMITS);
          if (!ok) {
            throw new Error(`stream 'consumer_limits' requires server ${min}`);
          }
        }
        function validateStreamSource(context, src) {
          const count = src?.subject_transforms?.length || 0;
          if (count > 0) {
            const { min, ok } = nci.features.get(internal_1.Feature.JS_STREAM_SOURCE_SUBJECT_TRANSFORM);
            if (!ok) {
              throw new Error(`${context} 'subject_transforms' requires server ${min}`);
            }
          }
        }
        if (cfg.sources) {
          cfg.sources.forEach((src) => {
            validateStreamSource("stream sources", src);
          });
        }
        if (cfg.mirror) {
          validateStreamSource("stream mirror", cfg.mirror);
        }
      }
      // mirrors server/jetstream_versioning.go:setStaticStreamMetadata
      minStreamApi(c) {
        if (c.allow_batched === true || c.mirror?.consumer || c.sources?.some((s) => s.consumer))
          return 4;
        if (c.allow_msg_counter === true || c.allow_atomic === true || c.allow_msg_schedules === true || c.persist_mode === jsapi_types_1.PersistMode.Async)
          return 2;
        if (c.allow_msg_ttl === true || typeof c.subject_delete_marker_ttl === "number" && c.subject_delete_marker_ttl > 0)
          return 1;
        return 0;
      }
      requiredApiOpts(c) {
        if (!this.sendRequiredApiLevel())
          return {};
        const minApiVersion = this.minStreamApi(c);
        return minApiVersion > 0 ? { minApiVersion } : {};
      }
      async add(cfg) {
        this.checkStreamConfigVersions(cfg);
        (0, jsutil_1.validateStreamName)(cfg.name);
        cfg.mirror = convertStreamSourceDomain(cfg.mirror);
        cfg.sources = cfg.sources?.map(convertStreamSourceDomain);
        const r = await this._request(`${this.prefix}.STREAM.CREATE.${cfg.name}`, cfg, this.requiredApiOpts(cfg));
        const si = r;
        this._fixInfo(si);
        return si;
      }
      async delete(stream) {
        (0, jsutil_1.validateStreamName)(stream);
        const r = await this._request(`${this.prefix}.STREAM.DELETE.${stream}`);
        const cr = r;
        return cr.success;
      }
      async update(name, cfg = {}) {
        if (typeof name === "object") {
          const sc = name;
          name = sc.name;
          cfg = sc;
          console.trace(`\x1B[33m >> streams.update(config: StreamConfig) api changed to streams.update(name: string, config: StreamUpdateConfig) - this shim will be removed - update your code.  \x1B[0m`);
        }
        this.checkStreamConfigVersions(cfg);
        (0, jsutil_1.validateStreamName)(name);
        const old = await this.info(name);
        const update = Object.assign(old.config, cfg);
        update.mirror = convertStreamSourceDomain(update.mirror);
        update.sources = update.sources?.map(convertStreamSourceDomain);
        const r = await this._request(`${this.prefix}.STREAM.UPDATE.${name}`, update, this.requiredApiOpts(cfg));
        const si = r;
        this._fixInfo(si);
        return si;
      }
      async info(name, data) {
        (0, jsutil_1.validateStreamName)(name);
        const subj = `${this.prefix}.STREAM.INFO.${name}`;
        const r = await this._request(subj, data);
        let si = r;
        let { total, limit } = si;
        let have = si.state.subjects ? Object.getOwnPropertyNames(si.state.subjects).length : 1;
        if (total && total > have) {
          const infos = [si];
          const paged = data || {};
          let i = 0;
          while (total > have) {
            i++;
            paged.offset = limit * i;
            const r2 = await this._request(subj, paged);
            total = r2.total;
            infos.push(r2);
            const count = Object.getOwnPropertyNames(r2.state.subjects).length;
            have += count;
            if (count < limit) {
              break;
            }
          }
          let subjects = {};
          for (let i2 = 0; i2 < infos.length; i2++) {
            si = infos[i2];
            if (si.state.subjects) {
              subjects = Object.assign(subjects, si.state.subjects);
            }
          }
          si.offset = 0;
          si.total = 0;
          si.limit = 0;
          si.state.subjects = subjects;
        }
        this._fixInfo(si);
        return si;
      }
      list(subject = "") {
        const payload = subject?.length ? { subject } : {};
        const listerFilter = (v) => {
          const slr = v;
          slr.streams.forEach((si) => {
            this._fixInfo(si);
          });
          return slr.streams;
        };
        const subj = `${this.prefix}.STREAM.LIST`;
        return new jslister_1.ListerImpl(subj, listerFilter, this, payload);
      }
      // FIXME: init of sealed, deny_delete, deny_purge shouldn't be necessary
      //  https://github.com/nats-io/nats-server/issues/2633
      _fixInfo(si) {
        si.config.sealed = si.config.sealed || false;
        si.config.deny_delete = si.config.deny_delete || false;
        si.config.deny_purge = si.config.deny_purge || false;
        si.config.allow_rollup_hdrs = si.config.allow_rollup_hdrs || false;
      }
      async purge(name, opts) {
        if (opts) {
          const { keep, seq } = opts;
          if (typeof keep === "number" && typeof seq === "number") {
            throw internal_1.InvalidArgumentError.format(["keep", "seq"], "are mutually exclusive");
          }
        }
        (0, jsutil_1.validateStreamName)(name);
        const v = await this._request(`${this.prefix}.STREAM.PURGE.${name}`, opts);
        return v;
      }
      async deleteMessage(stream, seq, erase = true) {
        (0, jsutil_1.validateStreamName)(stream);
        const dr = { seq };
        if (!erase) {
          dr.no_erase = true;
        }
        const r = await this._request(`${this.prefix}.STREAM.MSG.DELETE.${stream}`, dr);
        const cr = r;
        return cr.success;
      }
      async getMessage(stream, query) {
        (0, jsutil_1.validateStreamName)(stream);
        try {
          const r = await this._request(`${this.prefix}.STREAM.MSG.GET.${stream}`, query);
          const sm = r;
          return new StoredMsgImpl(sm);
        } catch (err) {
          if (err instanceof jserrors_1.JetStreamApiError && err.code === jserrors_1.JetStreamApiCodes.NoMessageFound) {
            return null;
          }
          return Promise.reject(err);
        }
      }
      find(subject) {
        return this.findStream(subject);
      }
      names(subject = "") {
        const payload = subject?.length ? { subject } : {};
        const listerFilter = (v) => {
          const sr = v;
          return sr.streams;
        };
        const subj = `${this.prefix}.STREAM.NAMES`;
        return new jslister_1.ListerImpl(subj, listerFilter, this, payload);
      }
      async get(name) {
        const si = await this.info(name);
        return Promise.resolve(new StreamImpl(this, si));
      }
    };
    exports.StreamAPIImpl = StreamAPIImpl;
    var StoredMsgImpl = class {
      _header;
      smr;
      static jc;
      constructor(smr) {
        this.smr = smr;
      }
      get pending() {
        return 0;
      }
      get lastSequence() {
        return 0;
      }
      get subject() {
        return this.smr.message.subject;
      }
      get seq() {
        return this.smr.message.seq;
      }
      get timestamp() {
        return this.smr.message.time;
      }
      get time() {
        return new Date(Date.parse(this.timestamp));
      }
      get data() {
        return this.smr.message.data ? this._parse(this.smr.message.data) : internal_1.Empty;
      }
      get header() {
        if (!this._header) {
          if (this.smr.message.hdrs) {
            const hd = this._parse(this.smr.message.hdrs);
            this._header = internal_1.MsgHdrsImpl.decode(hd);
          } else {
            this._header = (0, internal_1.headers)();
          }
        }
        return this._header;
      }
      _parse(s) {
        const bs = atob(s);
        const len = bs.length;
        const bytes = new Uint8Array(len);
        for (let i = 0; i < len; i++) {
          bytes[i] = bs.charCodeAt(i);
        }
        return bytes;
      }
      json(reviver) {
        return JSON.parse(new TextDecoder().decode(this.data), reviver);
      }
      string() {
        return internal_1.TD.decode(this.data);
      }
    };
    exports.StoredMsgImpl = StoredMsgImpl;
    var StreamsImpl = class {
      api;
      constructor(api) {
        this.api = api;
      }
      get(stream) {
        return this.api.info(stream).then((si) => {
          return new StreamImpl(this.api, si);
        });
      }
    };
    exports.StreamsImpl = StreamsImpl;
  }
});

// node_modules/@nats-io/jetstream/lib/jsm_direct.js
var require_jsm_direct = __commonJS({
  "node_modules/@nats-io/jetstream/lib/jsm_direct.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.DirectConsumer = exports.DirectMsgImpl = exports.DirectStreamAPIImpl = void 0;
    var jsbaseclient_api_1 = require_jsbaseclient_api();
    var types_1 = require_types2();
    var internal_1 = require_internal_mod();
    var jsutil_1 = require_jsutil();
    var jserrors_1 = require_jserrors();
    var DirectStreamAPIImpl = class extends jsbaseclient_api_1.BaseApiClientImpl {
      constructor(nc, opts) {
        super(nc, opts);
      }
      async getMessage(stream, query) {
        (0, jsutil_1.validateStreamName)(stream);
        if ("start_time" in query) {
          const { min, ok } = this.nc.features.get(internal_1.Feature.JS_BATCH_DIRECT_GET);
          if (!ok) {
            throw new Error(`start_time direct option require server ${min}`);
          }
        }
        let qq = query;
        const { last_by_subj } = qq;
        if (last_by_subj) {
          qq = null;
        }
        const payload = qq ? JSON.stringify(qq) : internal_1.Empty;
        const pre = this.opts.apiPrefix || "$JS.API";
        const subj = last_by_subj ? `${pre}.DIRECT.GET.${stream}.${last_by_subj}` : `${pre}.DIRECT.GET.${stream}`;
        const r = await this.nc.request(subj, payload, { timeout: this.timeout });
        if (r.headers?.code !== 0) {
          const status = new jserrors_1.JetStreamStatus(r);
          if (status.isMessageNotFound()) {
            return Promise.resolve(null);
          } else {
            return Promise.reject(status.toError());
          }
        }
        const dm = new DirectMsgImpl(r);
        return Promise.resolve(dm);
      }
      getBatch(stream, opts) {
        opts.batch = opts.batch || 1024;
        return this.get(stream, opts);
      }
      getLastMessagesFor(stream, opts) {
        return this.get(stream, opts);
      }
      get(stream, opts) {
        opts = { ...opts };
        const { min, ok } = this.nc.features.get(internal_1.Feature.JS_BATCH_DIRECT_GET);
        if (!ok) {
          return Promise.reject(new Error(`batch direct require server ${min}`));
        }
        (0, jsutil_1.validateStreamName)(stream);
        const callback = typeof opts.callback === "function" ? opts.callback : null;
        const iter = new internal_1.QueuedIteratorImpl();
        function pushIter(done, d) {
          if (done) {
            iter.push(() => {
              done.err ? iter.stop(done.err) : iter.stop();
            });
            return;
          }
          iter.push(d);
        }
        function pushCb(done, m) {
          const cb = callback;
          if (typeof m === "function") {
            m();
            return;
          }
          cb(done, m);
        }
        if (callback) {
          iter.iterClosed.then((err) => {
            push({ err: err ? err : void 0 }, {});
            sub.unsubscribe();
          });
        }
        const push = callback ? pushCb : pushIter;
        const inbox = (0, internal_1.createInbox)(this.nc.options.inboxPrefix);
        let batchSupported = false;
        const sub = this.nc.subscribe(inbox, {
          timeout: 5e3,
          callback: (err, msg) => {
            if (err) {
              iter.stop(err);
              sub.unsubscribe();
              return;
            }
            const status = jserrors_1.JetStreamStatus.maybeParseStatus(msg);
            if (status) {
              if (status.isNoResults()) {
                push({}, () => {
                  iter.stop();
                });
              }
              if (status.isEndOfBatch()) {
                push({}, () => {
                  iter.stop();
                });
              } else {
                const err2 = status.toError();
                push({ err: err2 }, () => {
                  iter.stop(err2);
                });
              }
              return;
            }
            if (!batchSupported) {
              if (typeof msg.headers?.get("Nats-Num-Pending") !== "string") {
                sub.unsubscribe();
                push({}, () => {
                  iter.stop();
                });
              } else {
                batchSupported = true;
              }
            }
            push(null, new DirectMsgImpl(msg));
          }
        });
        const pre = this.opts.apiPrefix || "$JS.API";
        const subj = `${pre}.DIRECT.GET.${stream}`;
        const payload = JSON.stringify(opts, (key, value) => {
          if ((key === "up_to_time" || key === "start_time") && value instanceof Date) {
            return value.toISOString();
          }
          return value;
        });
        this.nc.publish(subj, payload, { reply: inbox });
        return Promise.resolve(iter);
      }
    };
    exports.DirectStreamAPIImpl = DirectStreamAPIImpl;
    var DirectMsgImpl = class {
      data;
      header;
      constructor(m) {
        if (!m.headers) {
          throw new Error("headers expected");
        }
        this.data = m.data;
        this.header = m.headers;
      }
      get subject() {
        return this.header.last(types_1.DirectMsgHeaders.Subject);
      }
      get seq() {
        const v = this.header.last(types_1.DirectMsgHeaders.Sequence);
        return typeof v === "string" ? parseInt(v) : 0;
      }
      get time() {
        return new Date(Date.parse(this.timestamp));
      }
      get timestamp() {
        return this.header.last(types_1.DirectMsgHeaders.TimeStamp);
      }
      get stream() {
        return this.header.last(types_1.DirectMsgHeaders.Stream);
      }
      get lastSequence() {
        const v = this.header.last(types_1.DirectMsgHeaders.LastSequence);
        return typeof v === "string" ? parseInt(v) : 0;
      }
      get pending() {
        const v = this.header.last(types_1.DirectMsgHeaders.NumPending);
        return typeof v === "string" ? parseInt(v) : -1;
      }
      json(reviver) {
        return JSON.parse(new TextDecoder().decode(this.data), reviver);
      }
      string() {
        return internal_1.TD.decode(this.data);
      }
    };
    exports.DirectMsgImpl = DirectMsgImpl;
    function isDirectBatchStartTime(t) {
      return typeof t === "object" && "start_time" in t;
    }
    function isMaxBytes(t) {
      return typeof t === "object" && "max_bytes" in t;
    }
    var DirectConsumer = class {
      stream;
      api;
      cursor;
      listeners;
      start;
      constructor(stream, api, start) {
        this.stream = stream;
        this.api = api;
        this.cursor = { last: 0 };
        this.listeners = [];
        this.start = start;
      }
      getOptions(opts) {
        opts = opts || {};
        const dbo = {};
        if (this.cursor.last === 0) {
          if (isDirectBatchStartTime(this.start)) {
            dbo.start_time = this.start.start_time;
          } else {
            dbo.seq = this.start.seq || 1;
          }
        } else {
          dbo.seq = this.cursor.last + 1;
        }
        if (isMaxBytes(opts)) {
          dbo.max_bytes = opts.max_bytes;
        } else {
          dbo.batch = opts.batch ?? 100;
        }
        return dbo;
      }
      status() {
        const iter = new internal_1.QueuedIteratorImpl();
        this.listeners.push(iter);
        return iter;
      }
      notify(n) {
        if (this.listeners.length > 0) {
          (() => {
            const remove = [];
            this.listeners.forEach((l) => {
              const qi = l;
              if (!qi.done) {
                qi.push(n);
              } else {
                remove.push(qi);
              }
            });
            this.listeners = this.listeners.filter((l) => !remove.includes(l));
          })();
        }
      }
      debug() {
        console.log(this.cursor);
      }
      consume(opts) {
        let pending;
        let requestDone;
        const qi = new internal_1.QueuedIteratorImpl();
        (async () => {
          while (true) {
            if (this.cursor.pending === 0) {
              this.notify({
                type: "debug",
                code: 0,
                description: "sleeping for 2500"
              });
              pending = (0, internal_1.delay)(2500);
              await pending;
            }
            if (qi.done) {
              break;
            }
            requestDone = (0, internal_1.deferred)();
            const dbo = this.getOptions(opts);
            this.notify({
              type: "next",
              options: Object.assign({}, opts)
            });
            dbo.callback = (r, sm) => {
              if (r) {
                if (r.err) {
                  if (r.err instanceof jserrors_1.JetStreamStatusError) {
                    this.notify({
                      type: "debug",
                      code: r.err.code,
                      description: r.err.message
                    });
                  } else {
                    this.notify({
                      type: "debug",
                      code: 0,
                      description: r.err.message
                    });
                  }
                }
                requestDone.resolve();
              } else if (sm.lastSequence > 0 && sm.lastSequence !== this.cursor.last) {
                src.stop();
                requestDone.resolve();
                this.notify({
                  type: "reset",
                  name: "direct"
                });
              } else {
                qi.push(sm);
                qi.received++;
                this.cursor.last = sm.seq;
                this.cursor.pending = sm.pending;
              }
            };
            const src = await this.api.getBatch(this.stream, dbo);
            qi.iterClosed.then(() => {
              src.stop();
              pending?.cancel();
              requestDone?.resolve();
            });
            await requestDone;
          }
        })().catch((err) => {
          qi.stop(err);
        });
        return Promise.resolve(qi);
      }
      async fetch(opts) {
        const dbo = this.getOptions(opts);
        const qi = new internal_1.QueuedIteratorImpl();
        const src = await this.api.get(this.stream, Object.assign({
          callback: (done, sm) => {
            if (done) {
              qi.push(() => {
                done.err ? qi.stop(done.err) : qi.stop();
              });
            } else if (sm.lastSequence > 0 && sm.lastSequence !== this.cursor.last) {
              qi.push(() => {
                qi.stop();
              });
              src.stop();
            } else {
              qi.push(sm);
              qi.received++;
              this.cursor.last = sm.seq;
              this.cursor.pending = sm.pending;
            }
          }
        }, dbo));
        qi.iterClosed.then(() => {
          src.stop();
        });
        return qi;
      }
      async next() {
        const sm = await this.api.getMessage(this.stream, {
          seq: this.cursor.last + 1
        });
        const seq = sm?.seq;
        if (seq) {
          this.cursor.last = seq;
        }
        return sm;
      }
    };
    exports.DirectConsumer = DirectConsumer;
  }
});

// node_modules/@nats-io/jetstream/lib/jsclient.js
var require_jsclient = __commonJS({
  "node_modules/@nats-io/jetstream/lib/jsclient.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.BatchPublisherImpl = exports.JetStreamClientImpl = exports.JetStreamManagerImpl = void 0;
    exports.startFastIngest = startFastIngest;
    exports.toJetStreamClient = toJetStreamClient;
    exports.jetstream = jetstream2;
    exports.jetstreamManager = jetstreamManager2;
    exports.scheduleSpecToHeader = scheduleSpecToHeader;
    var jsbaseclient_api_1 = require_jsbaseclient_api();
    var jsmconsumer_api_1 = require_jsmconsumer_api();
    var internal_1 = require_internal_mod();
    var jsmstream_api_1 = require_jsmstream_api();
    var jsapi_types_1 = require_jsapi_types();
    var jserrors_1 = require_jserrors();
    var jsm_direct_1 = require_jsm_direct();
    function startFastIngest(nc, subj, payload, opts, defaultTimeout = 5e3) {
      const { ackInterval, allowGaps, inboxPrefix, maxOutstandingAcks, ...publishOpts } = opts;
      const prefix = inboxPrefix ?? "_INBOX";
      if (!prefix || /\s/.test(prefix) || /[*>]/.test(prefix)) {
        return Promise.reject(new Error(`inboxPrefix must be non-empty, no wildcards or whitespace (got "${prefix}")`));
      }
      const o = {
        ackInterval: ackInterval ?? 10,
        allowGaps,
        inboxPrefix: prefix,
        maxOutstandingAcks: Math.min(3, Math.max(1, maxOutstandingAcks ?? 2))
      };
      const fi = new FastIngestImpl(nc, o, subj, defaultTimeout);
      return fi.start(payload, publishOpts).then(() => fi);
    }
    function buildPublishHeaders(opts) {
      const expect = opts.expect || {};
      const mh = opts.headers || (0, internal_1.headers)();
      if (opts.msgID) {
        mh.set(jsapi_types_1.PubHeaders.MsgIdHdr, opts.msgID);
      }
      if (expect.lastMsgID) {
        mh.set(jsapi_types_1.PubHeaders.ExpectedLastMsgIdHdr, expect.lastMsgID);
      }
      if (expect.streamName) {
        mh.set(jsapi_types_1.PubHeaders.ExpectedStreamHdr, expect.streamName);
      }
      if (typeof expect.lastSequence === "number") {
        mh.set(jsapi_types_1.PubHeaders.ExpectedLastSeqHdr, `${expect.lastSequence}`);
      }
      if (typeof expect.lastSubjectSequence === "number") {
        mh.set(jsapi_types_1.PubHeaders.ExpectedLastSubjectSequenceHdr, `${expect.lastSubjectSequence}`);
      }
      if (expect.lastSubjectSequenceSubject) {
        mh.set(jsapi_types_1.PubHeaders.ExpectedLastSubjectSequenceSubjectHdr, expect.lastSubjectSequenceSubject);
      }
      if (opts.ttl) {
        mh.set(jsapi_types_1.PubHeaders.MessageTTL, `${opts.ttl}`);
      }
      if (opts.schedule && opts.cancelSchedule) {
        throw new Error("schedule and cancelSchedule are mutually exclusive");
      }
      if (opts.schedule) {
        const so = opts.schedule;
        if (so.specification) {
          mh.set(jsapi_types_1.PubHeaders.Schedule, scheduleSpecToHeader(so.specification));
        }
        if (so.target) {
          mh.set(jsapi_types_1.PubHeaders.ScheduleTarget, so.target);
        }
        if (so.source) {
          mh.set(jsapi_types_1.PubHeaders.ScheduleSource, so.source);
        }
        if (so.ttl) {
          mh.set(jsapi_types_1.PubHeaders.ScheduleTTL, so.ttl);
        }
        if (so.timezone) {
          mh.set(jsapi_types_1.PubHeaders.ScheduleTimeZone, so.timezone);
        }
        if (so.rollup) {
          mh.set(jsapi_types_1.PubHeaders.ScheduleRollup, so.rollup);
        }
      }
      if (opts.cancelSchedule) {
        mh.set(jsapi_types_1.PubHeaders.Scheduler, opts.cancelSchedule.scheduleSubject);
        mh.set(jsapi_types_1.PubHeaders.ScheduleNext, "purge");
      }
      return mh;
    }
    function toJetStreamClient(nc) {
      if (typeof nc.nc === "undefined") {
        return jetstream2(nc);
      }
      return nc;
    }
    function jetstream2(nc, opts = {}) {
      return new JetStreamClientImpl(nc, opts);
    }
    async function jetstreamManager2(nc, opts = {}) {
      const adm = new JetStreamManagerImpl(nc, opts);
      if (opts.checkAPI !== false) {
        try {
          await adm.getAccountInfo();
        } catch (err) {
          throw err;
        }
      }
      return adm;
    }
    var JetStreamManagerImpl = class extends jsbaseclient_api_1.BaseApiClientImpl {
      streams;
      consumers;
      direct;
      constructor(nc, opts) {
        super(nc, opts);
        this.streams = new jsmstream_api_1.StreamAPIImpl(nc, opts);
        this.consumers = new jsmconsumer_api_1.ConsumerAPIImpl(nc, opts);
        this.direct = new jsm_direct_1.DirectStreamAPIImpl(nc, opts);
      }
      async getAccountInfo() {
        const r = await this._request(`${this.prefix}.INFO`);
        return r;
      }
      jetstream() {
        return jetstream2(this.nc, this.getOptions());
      }
      advisories() {
        const iter = new internal_1.QueuedIteratorImpl();
        this.nc.subscribe(`$JS.EVENT.ADVISORY.>`, {
          callback: (err, msg) => {
            if (err) {
              throw err;
            }
            try {
              const d = this.parseJsResponse(msg);
              const chunks = d.type.split(".");
              const kind = chunks[chunks.length - 1];
              iter.push({ kind, data: d });
            } catch (err2) {
              iter.stop(err2);
            }
          }
        });
        return iter;
      }
    };
    exports.JetStreamManagerImpl = JetStreamManagerImpl;
    function scheduleSpecToHeader(spec) {
      if (typeof spec === "string") {
        return spec;
      }
      if (spec instanceof Date) {
        return `@at ${spec.toISOString()}`;
      }
      if ("at" in spec) {
        const iso = spec.at instanceof Date ? spec.at.toISOString() : spec.at;
        return `@at ${iso}`;
      }
      if ("every" in spec) {
        assertEveryAtLeastOneSecond(spec.every);
        return `@every ${spec.every}`;
      }
      if ("cron" in spec) {
        return spec.cron;
      }
      if ("predefined" in spec) {
        return spec.predefined;
      }
      throw new Error("invalid schedule specification");
    }
    function assertEveryAtLeastOneSecond(d) {
      const trimmed = d.trim();
      const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
      let totalMs = 0;
      let pos = 0;
      let m;
      while ((m = re.exec(trimmed)) !== null) {
        if (m.index !== pos)
          break;
        pos += m[0].length;
        const n = parseFloat(m[1]);
        const unit = m[2];
        totalMs += unit === "ns" ? n / 1e6 : unit === "us" || unit === "\xB5s" ? n / 1e3 : unit === "ms" ? n : unit === "s" ? n * 1e3 : unit === "m" ? n * 6e4 : n * 36e5;
      }
      if (trimmed === "" || pos !== trimmed.length) {
        throw new Error(`@every: unrecognized duration format: "${d}"`);
      }
      if (totalMs < 1e3) {
        throw new Error("@every interval must be at least 1s");
      }
    }
    var JetStreamClientImpl = class extends jsbaseclient_api_1.BaseApiClientImpl {
      consumers;
      streams;
      consumerAPI;
      streamAPI;
      constructor(nc, opts) {
        super(nc, opts);
        this.consumerAPI = new jsmconsumer_api_1.ConsumerAPIImpl(nc, opts);
        this.streamAPI = new jsmstream_api_1.StreamAPIImpl(nc, opts);
        this.consumers = new jsmstream_api_1.ConsumersImpl(this.consumerAPI);
        this.streams = new jsmstream_api_1.StreamsImpl(this.streamAPI);
      }
      jetstreamManager(checkAPI) {
        if (checkAPI === void 0) {
          checkAPI = this.opts.checkAPI;
        }
        const opts = Object.assign({}, this.opts, { checkAPI });
        try {
          (0, internal_1.createInbox)(opts.watcherPrefix);
        } catch (err) {
          return Promise.reject(err);
        }
        return jetstreamManager2(this.nc, opts);
      }
      get apiPrefix() {
        return this.prefix;
      }
      startBatch(subj, payload, opts) {
        const d = (0, internal_1.deferred)();
        const bp = new BatchPublisherImpl(this);
        bp.first(subj, payload, opts).then(() => {
          d.resolve(bp);
        }).catch((err) => {
          d.reject(err);
        });
        return d;
      }
      async _publish(subj, data = internal_1.Empty, opts) {
        opts = opts || {};
        opts = { ...opts };
        if (opts.cancelSchedule && opts.cancelSchedule.scheduleSubject === subj) {
          throw new Error("cancelSchedule.scheduleSubject must not equal the publish subject");
        }
        const mh = buildPublishHeaders(opts);
        const to = opts.timeout || this.timeout;
        const ro = {};
        if (to) {
          ro.timeout = to;
        }
        if (opts) {
          ro.headers = mh;
        }
        let { retries } = opts;
        retries = retries || 1;
        const bo = (0, internal_1.backoff)();
        let r = null;
        for (let i = 0; i < retries; i++) {
          try {
            r = await this.nc.request(subj, data, ro);
            break;
          } catch (err) {
            const re = err instanceof internal_1.RequestError ? err : null;
            if ((err instanceof internal_1.errors.TimeoutError || re?.isNoResponders()) && i + 1 < retries) {
              await (0, internal_1.delay)(bo.backoff(i));
            } else {
              throw re?.isNoResponders() ? new jserrors_1.JetStreamNotEnabled(`jetstream is not enabled`, {
                cause: err
              }) : err;
            }
          }
        }
        return r;
      }
      async publish(subj, data = internal_1.Empty, opts) {
        const r = await this._publish(subj, data, opts);
        const pa = this.parseJsResponse(r);
        if (pa.stream === "") {
          throw new jserrors_1.JetStreamError("invalid ack response");
        }
        pa.duplicate = pa.duplicate ? pa.duplicate : false;
        return pa;
      }
    };
    exports.JetStreamClientImpl = JetStreamClientImpl;
    var BatchPublisherImpl = class {
      nc;
      js;
      id;
      count;
      done;
      constructor(js) {
        this.count = 0;
        this.id = internal_1.nuid.next();
        this.js = js;
        this.nc = this.js.nc;
        this.done = false;
      }
      async first(subj, payload, opts) {
        opts = opts || {};
        opts.headers = opts?.headers || (0, internal_1.headers)();
        opts.headers.set("Nats-Batch-Id", this.id);
        this.count++;
        opts.headers.set("Nats-Batch-Sequence", this.count.toString());
        const r = await this.js._publish(subj, payload, opts);
        if (r.data.length > 0) {
          this.js.parseJsResponse(r);
        }
      }
      add(subj, payload, opts = { ack: false }) {
        if (this.done) {
          throw new Error("batch publisher is done");
        }
        opts.headers = opts?.headers || (0, internal_1.headers)();
        opts.headers.set("Nats-Batch-Id", this.id);
        this.count++;
        opts.headers.set("Nats-Batch-Sequence", this.count.toString());
        const hasAck = "ack" in opts && opts.ack === true;
        if (hasAck) {
          const d = (0, internal_1.deferred)();
          this.js._publish(subj, payload, {
            headers: opts.headers,
            timeout: opts.timeout
          }).then((m) => {
            if (m.data.length > 0) {
              this.js.parseJsResponse(m);
            }
            d.resolve();
          }).catch((err) => {
            this.done = true;
            d.reject(err);
          });
          return d;
        } else {
          return this.nc.publish(subj, payload, { headers: opts.headers });
        }
      }
      async commit(subj, payload, opts = {}) {
        if (this.done) {
          throw new Error("batch publisher is done");
        } else {
          this.done = true;
        }
        opts.headers = opts?.headers || (0, internal_1.headers)();
        opts.headers.set("Nats-Batch-Id", this.id);
        this.count++;
        opts.headers.set("Nats-Batch-Sequence", this.count.toString());
        opts.headers.set("Nats-Batch-Commit", "1");
        const r = await this.js._publish(subj, payload, {
          headers: opts.headers,
          timeout: opts.timeout || 0
        });
        const ack = r.json();
        if (ack.count !== this.count) {
          throw new Error("batch didn't contain number of published messages");
        }
        return ack;
      }
    };
    exports.BatchPublisherImpl = BatchPublisherImpl;
    var BATCH_CLOSED = "batch closed";
    var FastIngestOp = {
      Start: 0,
      Append: 1,
      Final: 2,
      EOB: 3,
      Ping: 4
    };
    var FastIngestImpl = class {
      batch;
      nc;
      batchSubj;
      gapMode;
      initialFlow;
      seq;
      acked;
      ackInterval;
      inboxPrefix;
      maxOutstandingAcks;
      defaultTimeout;
      gapIter;
      sub;
      pending;
      closed;
      closeErr;
      startDeferred;
      closedDeferred;
      constructor(nc, opts, firstSubj, defaultTimeout) {
        this.nc = nc;
        this.batchSubj = firstSubj;
        this.gapMode = opts.allowGaps ? "ok" : "fail";
        this.initialFlow = opts.ackInterval;
        this.inboxPrefix = opts.inboxPrefix;
        this.maxOutstandingAcks = opts.maxOutstandingAcks;
        this.defaultTimeout = defaultTimeout;
        this.batch = internal_1.nuid.next();
        this.seq = 0;
        this.acked = 0;
        this.ackInterval = opts.ackInterval;
        this.pending = /* @__PURE__ */ new Map();
        this.closed = false;
        this.startDeferred = (0, internal_1.deferred)();
        this.closedDeferred = (0, internal_1.deferred)();
        this.closedDeferred.catch(() => {
        });
        this.startDeferred.catch(() => {
        });
        const inbox = `${this.inboxPrefix}.${this.batch}.>`;
        this.sub = this.nc.subscribe(inbox, {
          callback: (err, msg) => this.route(err, msg)
        });
      }
      replyFor(op, seq) {
        return `${this.inboxPrefix}.${this.batch}.${this.initialFlow}.${this.gapMode}.${seq}.${op}.$FI`;
      }
      start(payload, opts) {
        this.seq = 1;
        const rs = this.replyFor(FastIngestOp.Start, 1);
        const headers2 = opts ? buildPublishHeaders(opts) : void 0;
        this.nc.publish(this.batchSubj, payload, { reply: rs, headers: headers2 });
        return this.deadlineOrClose(this.startDeferred, opts?.timeout ?? this.defaultTimeout);
      }
      deadlineOrClose(p, ms) {
        return (0, internal_1.deadline)(p, ms).catch((err) => {
          if (!this.closed)
            this.close(err);
          throw err;
        });
      }
      route(err, m) {
        if (err) {
          this.close(err);
          return;
        }
        let data;
        try {
          data = (0, jsbaseclient_api_1.parseJsResponse)(m);
        } catch (err2) {
          this.close(err2);
          return;
        }
        if ("batch" in data && typeof data.batch === "string") {
          const ack = data;
          const e = this.pending.get(m.subject);
          if (e && (e.op === FastIngestOp.Final || e.op === FastIngestOp.EOB)) {
            e.deferred.resolve(ack);
            this.pending.delete(m.subject);
          }
          for (const [, entry] of this.pending) {
            if (entry.op === FastIngestOp.Append || entry.op === FastIngestOp.Ping) {
              entry.deferred.resolve({ batchSeq: entry.seq, ackSeq: this.acked });
            } else {
              entry.deferred.reject(new Error(BATCH_CLOSED));
            }
          }
          this.pending.clear();
          this.resolveStart();
          this.closedDeferred.resolve(ack);
          this.closed = true;
          this.gapIter?.stop();
          this.sub.unsubscribe();
          return;
        }
        const typed = data;
        if (typed.type === "gap") {
          if (this.gapIter) {
            const g = data;
            this.gapIter.push({ lastSeq: g.last_seq, seq: g.seq });
          }
          return;
        }
        const fa = data;
        if (typeof fa.msgs === "number")
          this.ackInterval = fa.msgs;
        if (typeof fa.seq === "number" && fa.seq > this.acked) {
          this.acked = fa.seq;
        }
        this.resolveStart();
        const exact = this.pending.get(m.subject);
        if (exact && (exact.op === FastIngestOp.Append || exact.op === FastIngestOp.Ping)) {
          exact.deferred.resolve({ batchSeq: exact.seq, ackSeq: this.acked });
          this.pending.delete(m.subject);
        }
        for (const [rs, e] of this.pending) {
          if (e.op === FastIngestOp.Append && e.seq - this.acked < this.ackInterval * this.maxOutstandingAcks) {
            e.deferred.resolve({ batchSeq: e.seq, ackSeq: this.acked });
            this.pending.delete(rs);
          }
        }
      }
      resolveStart() {
        this.startDeferred.resolve();
      }
      close(err) {
        this.closed = true;
        this.closeErr = err;
        for (const [, e] of this.pending)
          e.deferred.reject(err);
        this.pending.clear();
        this.startDeferred.reject(err);
        this.closedDeferred.reject(err);
        this.gapIter?.stop();
        this.sub.unsubscribe();
      }
      add(subj, payload, opts) {
        if (this.closed)
          return Promise.reject(new Error(BATCH_CLOSED));
        const mySeq = ++this.seq;
        const rs = this.replyFor(FastIngestOp.Append, mySeq);
        const headers2 = opts ? buildPublishHeaders(opts) : void 0;
        this.nc.publish(subj, payload, { reply: rs, headers: headers2 });
        if (mySeq - this.acked < this.ackInterval * this.maxOutstandingAcks) {
          return Promise.resolve({ batchSeq: mySeq, ackSeq: this.acked });
        }
        const d = (0, internal_1.deferred)();
        this.pending.set(rs, { seq: mySeq, op: FastIngestOp.Append, deferred: d });
        return this.deadlineOrClose(d, opts?.timeout ?? this.defaultTimeout);
      }
      last(subj, payload, opts) {
        if (this.closed)
          return Promise.reject(new Error(BATCH_CLOSED));
        const mySeq = ++this.seq;
        const rs = this.replyFor(FastIngestOp.Final, mySeq);
        const d = (0, internal_1.deferred)();
        this.pending.set(rs, { seq: mySeq, op: FastIngestOp.Final, deferred: d });
        const headers2 = opts ? buildPublishHeaders(opts) : void 0;
        this.nc.publish(subj, payload, { reply: rs, headers: headers2 });
        return this.deadlineOrClose(d, opts?.timeout ?? this.defaultTimeout);
      }
      end(opts) {
        if (this.closed)
          return Promise.reject(new Error(BATCH_CLOSED));
        const mySeq = ++this.seq;
        const rs = this.replyFor(FastIngestOp.EOB, mySeq);
        const d = (0, internal_1.deferred)();
        this.pending.set(rs, { seq: mySeq, op: FastIngestOp.EOB, deferred: d });
        const headers2 = opts ? buildPublishHeaders(opts) : void 0;
        this.nc.publish(this.batchSubj, internal_1.Empty, { reply: rs, headers: headers2 });
        return this.deadlineOrClose(d, opts?.timeout ?? this.defaultTimeout);
      }
      ping(timeout = this.defaultTimeout) {
        if (this.closed)
          return Promise.reject(new Error(BATCH_CLOSED));
        const rs = this.replyFor(FastIngestOp.Ping, this.seq);
        const existing = this.pending.get(rs);
        if (existing && existing.op === FastIngestOp.Ping) {
          return (0, internal_1.deadline)(existing.deferred, timeout);
        }
        const d = (0, internal_1.deferred)();
        this.pending.set(rs, { seq: this.seq, op: FastIngestOp.Ping, deferred: d });
        this.nc.publish(this.batchSubj, internal_1.Empty, { reply: rs });
        return this.deadlineOrClose(d, timeout);
      }
      done() {
        return this.closedDeferred;
      }
      gaps() {
        if (!this.gapIter) {
          this.gapIter = new internal_1.QueuedIteratorImpl();
        }
        return this.gapIter;
      }
    };
  }
});

// node_modules/@nats-io/jetstream/lib/internal_mod.js
var require_internal_mod2 = __commonJS({
  "node_modules/@nats-io/jetstream/lib/internal_mod.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.validateStreamName = exports.jserrors = exports.JetStreamStatusError = exports.JetStreamStatus = exports.JetStreamError = exports.JetStreamApiError = exports.JetStreamApiCodes = exports.isMessageNotFound = exports.ListerImpl = exports.StoreCompression = exports.StorageType = exports.RetentionPolicy = exports.ReplayPolicy = exports.PubHeaders = exports.PersistMode = exports.DiscardPolicy = exports.DeliverPolicy = exports.AckPolicy = exports.DirectMsgImpl = exports.BaseApiClientImpl = exports.toJetStreamClient = exports.startFastIngest = exports.jetstreamManager = exports.JetStreamClientImpl = exports.jetstream = exports.RepublishHeaders = exports.JsHeaders = exports.isPushConsumer = exports.isPullConsumer = exports.isOrderedPushConsumerOptions = exports.isBoundPushConsumerOptions = exports.DirectMsgHeaders = exports.AdvisoryKind = void 0;
    var types_1 = require_types2();
    Object.defineProperty(exports, "AdvisoryKind", { enumerable: true, get: function() {
      return types_1.AdvisoryKind;
    } });
    Object.defineProperty(exports, "DirectMsgHeaders", { enumerable: true, get: function() {
      return types_1.DirectMsgHeaders;
    } });
    Object.defineProperty(exports, "isBoundPushConsumerOptions", { enumerable: true, get: function() {
      return types_1.isBoundPushConsumerOptions;
    } });
    Object.defineProperty(exports, "isOrderedPushConsumerOptions", { enumerable: true, get: function() {
      return types_1.isOrderedPushConsumerOptions;
    } });
    Object.defineProperty(exports, "isPullConsumer", { enumerable: true, get: function() {
      return types_1.isPullConsumer;
    } });
    Object.defineProperty(exports, "isPushConsumer", { enumerable: true, get: function() {
      return types_1.isPushConsumer;
    } });
    Object.defineProperty(exports, "JsHeaders", { enumerable: true, get: function() {
      return types_1.JsHeaders;
    } });
    Object.defineProperty(exports, "RepublishHeaders", { enumerable: true, get: function() {
      return types_1.RepublishHeaders;
    } });
    var jsclient_1 = require_jsclient();
    Object.defineProperty(exports, "jetstream", { enumerable: true, get: function() {
      return jsclient_1.jetstream;
    } });
    Object.defineProperty(exports, "JetStreamClientImpl", { enumerable: true, get: function() {
      return jsclient_1.JetStreamClientImpl;
    } });
    Object.defineProperty(exports, "jetstreamManager", { enumerable: true, get: function() {
      return jsclient_1.jetstreamManager;
    } });
    Object.defineProperty(exports, "startFastIngest", { enumerable: true, get: function() {
      return jsclient_1.startFastIngest;
    } });
    Object.defineProperty(exports, "toJetStreamClient", { enumerable: true, get: function() {
      return jsclient_1.toJetStreamClient;
    } });
    var jsbaseclient_api_1 = require_jsbaseclient_api();
    Object.defineProperty(exports, "BaseApiClientImpl", { enumerable: true, get: function() {
      return jsbaseclient_api_1.BaseApiClientImpl;
    } });
    var jsm_direct_1 = require_jsm_direct();
    Object.defineProperty(exports, "DirectMsgImpl", { enumerable: true, get: function() {
      return jsm_direct_1.DirectMsgImpl;
    } });
    var jsapi_types_1 = require_jsapi_types();
    Object.defineProperty(exports, "AckPolicy", { enumerable: true, get: function() {
      return jsapi_types_1.AckPolicy;
    } });
    Object.defineProperty(exports, "DeliverPolicy", { enumerable: true, get: function() {
      return jsapi_types_1.DeliverPolicy;
    } });
    Object.defineProperty(exports, "DiscardPolicy", { enumerable: true, get: function() {
      return jsapi_types_1.DiscardPolicy;
    } });
    Object.defineProperty(exports, "PersistMode", { enumerable: true, get: function() {
      return jsapi_types_1.PersistMode;
    } });
    Object.defineProperty(exports, "PubHeaders", { enumerable: true, get: function() {
      return jsapi_types_1.PubHeaders;
    } });
    Object.defineProperty(exports, "ReplayPolicy", { enumerable: true, get: function() {
      return jsapi_types_1.ReplayPolicy;
    } });
    Object.defineProperty(exports, "RetentionPolicy", { enumerable: true, get: function() {
      return jsapi_types_1.RetentionPolicy;
    } });
    Object.defineProperty(exports, "StorageType", { enumerable: true, get: function() {
      return jsapi_types_1.StorageType;
    } });
    Object.defineProperty(exports, "StoreCompression", { enumerable: true, get: function() {
      return jsapi_types_1.StoreCompression;
    } });
    var jslister_1 = require_jslister();
    Object.defineProperty(exports, "ListerImpl", { enumerable: true, get: function() {
      return jslister_1.ListerImpl;
    } });
    var jserrors_1 = require_jserrors();
    Object.defineProperty(exports, "isMessageNotFound", { enumerable: true, get: function() {
      return jserrors_1.isMessageNotFound;
    } });
    Object.defineProperty(exports, "JetStreamApiCodes", { enumerable: true, get: function() {
      return jserrors_1.JetStreamApiCodes;
    } });
    Object.defineProperty(exports, "JetStreamApiError", { enumerable: true, get: function() {
      return jserrors_1.JetStreamApiError;
    } });
    Object.defineProperty(exports, "JetStreamError", { enumerable: true, get: function() {
      return jserrors_1.JetStreamError;
    } });
    Object.defineProperty(exports, "JetStreamStatus", { enumerable: true, get: function() {
      return jserrors_1.JetStreamStatus;
    } });
    Object.defineProperty(exports, "JetStreamStatusError", { enumerable: true, get: function() {
      return jserrors_1.JetStreamStatusError;
    } });
    Object.defineProperty(exports, "jserrors", { enumerable: true, get: function() {
      return jserrors_1.jserrors;
    } });
    var jsutil_1 = require_jsutil();
    Object.defineProperty(exports, "validateStreamName", { enumerable: true, get: function() {
      return jsutil_1.validateStreamName;
    } });
  }
});

// node_modules/@nats-io/jetstream/lib/mod.js
var require_mod4 = __commonJS({
  "node_modules/@nats-io/jetstream/lib/mod.js"(exports) {
    "use strict";
    Object.defineProperty(exports, "__esModule", { value: true });
    exports.StoreCompression = exports.StorageType = exports.RetentionPolicy = exports.RepublishHeaders = exports.ReplayPolicy = exports.PubHeaders = exports.PersistMode = exports.JsHeaders = exports.JetStreamError = exports.JetStreamApiError = exports.JetStreamApiCodes = exports.isPushConsumer = exports.isPullConsumer = exports.DiscardPolicy = exports.DirectMsgHeaders = exports.DeliverPolicy = exports.AdvisoryKind = exports.AckPolicy = exports.jetstreamManager = exports.jetstream = void 0;
    var internal_mod_1 = require_internal_mod2();
    Object.defineProperty(exports, "jetstream", { enumerable: true, get: function() {
      return internal_mod_1.jetstream;
    } });
    Object.defineProperty(exports, "jetstreamManager", { enumerable: true, get: function() {
      return internal_mod_1.jetstreamManager;
    } });
    var internal_mod_2 = require_internal_mod2();
    Object.defineProperty(exports, "AckPolicy", { enumerable: true, get: function() {
      return internal_mod_2.AckPolicy;
    } });
    Object.defineProperty(exports, "AdvisoryKind", { enumerable: true, get: function() {
      return internal_mod_2.AdvisoryKind;
    } });
    Object.defineProperty(exports, "DeliverPolicy", { enumerable: true, get: function() {
      return internal_mod_2.DeliverPolicy;
    } });
    Object.defineProperty(exports, "DirectMsgHeaders", { enumerable: true, get: function() {
      return internal_mod_2.DirectMsgHeaders;
    } });
    Object.defineProperty(exports, "DiscardPolicy", { enumerable: true, get: function() {
      return internal_mod_2.DiscardPolicy;
    } });
    Object.defineProperty(exports, "isPullConsumer", { enumerable: true, get: function() {
      return internal_mod_2.isPullConsumer;
    } });
    Object.defineProperty(exports, "isPushConsumer", { enumerable: true, get: function() {
      return internal_mod_2.isPushConsumer;
    } });
    Object.defineProperty(exports, "JetStreamApiCodes", { enumerable: true, get: function() {
      return internal_mod_2.JetStreamApiCodes;
    } });
    Object.defineProperty(exports, "JetStreamApiError", { enumerable: true, get: function() {
      return internal_mod_2.JetStreamApiError;
    } });
    Object.defineProperty(exports, "JetStreamError", { enumerable: true, get: function() {
      return internal_mod_2.JetStreamError;
    } });
    Object.defineProperty(exports, "JsHeaders", { enumerable: true, get: function() {
      return internal_mod_2.JsHeaders;
    } });
    Object.defineProperty(exports, "PersistMode", { enumerable: true, get: function() {
      return internal_mod_2.PersistMode;
    } });
    Object.defineProperty(exports, "PubHeaders", { enumerable: true, get: function() {
      return internal_mod_2.PubHeaders;
    } });
    Object.defineProperty(exports, "ReplayPolicy", { enumerable: true, get: function() {
      return internal_mod_2.ReplayPolicy;
    } });
    Object.defineProperty(exports, "RepublishHeaders", { enumerable: true, get: function() {
      return internal_mod_2.RepublishHeaders;
    } });
    Object.defineProperty(exports, "RetentionPolicy", { enumerable: true, get: function() {
      return internal_mod_2.RetentionPolicy;
    } });
    Object.defineProperty(exports, "StorageType", { enumerable: true, get: function() {
      return internal_mod_2.StorageType;
    } });
    Object.defineProperty(exports, "StoreCompression", { enumerable: true, get: function() {
      return internal_mod_2.StoreCompression;
    } });
  }
});

// entry.mjs
var import_nats_core = __toESM(require_mod3(), 1);
var import_jetstream = __toESM(require_mod4(), 1);
var export_AckPolicy = import_jetstream.AckPolicy;
var export_headers = import_nats_core.headers;
var export_jetstream = import_jetstream.jetstream;
var export_jetstreamManager = import_jetstream.jetstreamManager;
var export_tokenAuthenticator = import_nats_core.tokenAuthenticator;
var export_wsconnect = import_nats_core.wsconnect;
export {
  export_AckPolicy as AckPolicy,
  export_headers as headers,
  export_jetstream as jetstream,
  export_jetstreamManager as jetstreamManager,
  export_tokenAuthenticator as tokenAuthenticator,
  export_wsconnect as wsconnect
};
