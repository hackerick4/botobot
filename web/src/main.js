// BotoBot site — enhancement layer.
//
// The page is fully readable and usable with no JS: copy text is selectable,
// every section has a static illustration, and the hero shows a static 5-panel
// storyboard. This module adds: one-tap copy (with a toast), scroll reveals, a
// scroll-scrubbed "story" connector, a looping privacy animation, the sticky
// CTAs, and the hero explainer — a GSAP timeline that drives an inline SVG
// through five captioned steps with clickable step dots and play/pause/replay.
//
// GSAP + ScrollTrigger are the only dependency; there is no WebGL/canvas and no
// eval, so the single bundle runs under the strict CSP (script-src 'self').
import { gsap } from "gsap";
import { ScrollTrigger } from "gsap/ScrollTrigger";

const root = document.documentElement;
root.classList.add("js");

const reduceMotion =
  window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
if (reduceMotion) root.classList.add("reduce");

const zh = (root.lang || "").toLowerCase().startsWith("zh");

gsap.registerPlugin(ScrollTrigger);

/* ---- copy-to-clipboard (+ toast) ----------------------------------------- */
// One implementation for every [data-copy] button. On success it shows the
// shared toast (an aria-live region) and, when present, swaps the button label.
// Falls back to selecting the text when the async clipboard API is missing.
function wireCopy() {
  const toast = document.getElementById("toast");
  if (toast) toast.removeAttribute("hidden");
  let toastTimer;
  const showToast = () => {
    if (!toast) return;
    toast.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toast.classList.remove("show"), 2600);
  };

  document.querySelectorAll(".copy[data-copy]").forEach((btn) => {
    const target = document.querySelector(btn.getAttribute("data-copy"));
    if (!target) return;
    const original = btn.textContent;
    const done = btn.getAttribute("data-label-done");
    let timer;
    const confirm = () => {
      showToast();
      if (done) {
        btn.textContent = done;
        btn.setAttribute("data-copied", "");
        clearTimeout(timer);
        timer = setTimeout(() => {
          btn.textContent = original;
          btn.removeAttribute("data-copied");
        }, 2000);
      }
    };
    const fallback = () => {
      const r = document.createRange();
      r.selectNodeContents(target);
      const sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(r);
      try {
        document.execCommand("copy");
        confirm();
      } catch (e) {
        /* leave it selected for a manual copy */
      }
    };
    btn.addEventListener("click", () => {
      const text = (target.textContent || "").trim();
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(confirm, fallback);
      } else {
        fallback();
      }
    });
  });
}

/* ---- sticky CTAs ---------------------------------------------------------- */
// Header CTA button appears once the hero scrolls out of view (desktop); a
// bottom bar does the same job on mobile and hides again over the final CTA
// band so it never covers the closing call to action.
function wireStickyCTA() {
  const hero = document.querySelector(".hero");
  const finalCta = document.querySelector(".cta-final");
  const headCta = document.querySelector(".head-cta");
  const bar = document.getElementById("sticky-cta");
  if (!hero || !("IntersectionObserver" in window)) return;
  if (bar) bar.removeAttribute("hidden");

  let heroIn = true;
  let finalIn = false;
  const update = () => {
    if (headCta) headCta.classList.toggle("show", !heroIn);
    if (bar) bar.classList.toggle("show", !heroIn && !finalIn);
  };
  new IntersectionObserver(
    ([e]) => {
      heroIn = e.isIntersecting;
      update();
    },
    { rootMargin: "-40% 0px 0px 0px" }
  ).observe(hero);
  if (finalCta) {
    new IntersectionObserver(
      ([e]) => {
        finalIn = e.isIntersecting;
        update();
      },
      { threshold: 0.01 }
    ).observe(finalCta);
  }
}

/* ---- scroll reveals ------------------------------------------------------ */
// CSS owns the motion (opacity/transform transitions on `.in`); we only toggle
// the class as elements enter. Disabled under reduced motion (CSS start-states
// are gated on html.js:not(.reduce), so content stays visible either way).
function wireReveals() {
  if (reduceMotion) return;
  const items = document.querySelectorAll("[data-rise], .step");
  if (!("IntersectionObserver" in window) || !items.length) {
    items.forEach((el) => el.classList.add("in"));
    return;
  }
  const io = new IntersectionObserver(
    (entries) => {
      entries.forEach((e) => {
        if (e.isIntersecting) {
          e.target.classList.add("in");
          io.unobserve(e.target);
        }
      });
    },
    { rootMargin: "0px 0px -12% 0px", threshold: 0.15 }
  );
  items.forEach((el) => io.observe(el));
}

/* ---- story connector (scroll-scrubbed) ----------------------------------- */
function wireStory() {
  const track = document.querySelector("[data-story]");
  const fill = document.getElementById("wire-fill");
  const env = document.getElementById("wire-envelope");
  const steps = Array.prototype.slice.call(document.querySelectorAll(".step"));
  if (!track || !fill || !env || !steps.length) return;

  const wireVisible = () => window.matchMedia("(min-width: 781px)").matches;

  const apply = (p) => {
    const pct = Math.max(0, Math.min(1, p));
    fill.style.width = 84 * pct + "%";
    env.style.left = 8 + 84 * pct + "%";
    const active = Math.min(steps.length - 1, Math.floor(pct * steps.length + 0.0001));
    steps.forEach((s, i) => {
      if (i <= active && pct > 0.001) s.setAttribute("data-active", "");
      else s.removeAttribute("data-active");
    });
  };

  if (reduceMotion) {
    apply(1);
    return;
  }

  ScrollTrigger.create({
    trigger: track,
    start: "top 72%",
    end: "bottom 72%",
    scrub: 0.4,
    onUpdate: (self) => {
      if (!wireVisible()) return;
      apply(self.progress);
    },
    onRefresh: (self) => {
      if (wireVisible()) apply(self.progress);
    },
  });
}

/* ---- privacy animation (in-view loop) ------------------------------------ */
function wirePrivacy() {
  const stage = document.querySelector("[data-privacy]");
  if (!stage) return;
  const $ = (id) => stage.querySelector("#" + id);
  const packet = $("pv-packet");
  const lockR = $("pv-lockR");
  const msgR = $("pv-msgR");
  const lockL = $("pv-lockL");
  const scramble = $("pv-scramble");
  if (!packet || !lockR || !msgR) return;

  const finalState = () => {
    gsap.set(packet, { x: 460, y: 130, opacity: 0 });
    gsap.set(msgR, { opacity: 1 });
    gsap.set(lockR, { rotation: 0, transformOrigin: "50% 50%" });
  };

  if (reduceMotion) {
    finalState();
    return;
  }

  const tl = gsap.timeline({ repeat: -1, repeatDelay: 0.7, paused: true });
  tl.set(packet, { x: 190, y: 130, opacity: 0 })
    .set(msgR, { opacity: 0 })
    .set(lockL, { scale: 0.4, opacity: 0, transformOrigin: "50% 50%" })
    .set(scramble, { opacity: 0.25 });
  tl.to(lockL, { scale: 1, opacity: 1, duration: 0.5, ease: "back.out(2)" }, 0.2);
  tl.to(packet, { opacity: 1, duration: 0.25 }, 0.8);
  tl.to(packet, { x: 320, duration: 1.1, ease: "power1.inOut" }, 0.95);
  tl.to(scramble, { opacity: 1, duration: 0.3, yoyo: true, repeat: 3 }, 2.0);
  tl.to(packet, { x: 460, duration: 1.1, ease: "power1.inOut" }, 2.9);
  tl.to(packet, { opacity: 0, duration: 0.25 }, 3.9);
  tl.to(lockR, { rotation: -14, duration: 0.4, ease: "back.out(2)", transformOrigin: "50% 50%" }, 3.9);
  tl.to(msgR, { opacity: 1, duration: 0.4 }, 4.05);
  tl.to({}, { duration: 0.8 });
  tl.set([lockL, lockR, msgR, scramble], { clearProps: "all" });

  ScrollTrigger.create({
    trigger: stage,
    start: "top 80%",
    end: "bottom 20%",
    onEnter: () => tl.play(),
    onEnterBack: () => tl.play(),
    onLeave: () => tl.pause(),
    onLeaveBack: () => tl.pause(),
  });
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) tl.pause();
    else if (ScrollTrigger.isInViewport && ScrollTrigger.isInViewport(stage)) tl.play();
  });
}

/* ---- one to one: two bots, one private line (v0.6.0) -------------------- */
// The regular bridge next to the public entry: two bots, each on its own
// computer, one line through the cloud; a generic message goes over and the
// reply comes back. Static markup is the no-JS / reduced-motion state.
function wireOne() {
  const stage = document.querySelector("[data-one]");
  if (!stage) return;
  const line = stage.querySelector("#po-line");
  const under = stage.querySelector(".po-under");
  const env = stage.querySelector("#po-env");
  const bots = [0, 1].map((i) => stage.querySelector("#po-b" + i));
  if (reduceMotion || !line || !line.getTotalLength || !under || !env || bots.some((b) => !b)) return;
  const w = env.querySelector("text");
  const dot = env.querySelector("circle");
  const wordIn = (w && w.getAttribute("data-in")) || "";
  const wordOut = stage.getAttribute("data-out") || "";
  const len = line.getTotalLength();
  const travel = (tl, at, from, to, word, color) => {
    const p = { t: from };
    const place = () => {
      // keep the bubble off both computers
      const pt = line.getPointAtLength(len * (0.2 + 0.6 * p.t));
      gsap.set(env, { x: pt.x, y: pt.y });
    };
    tl.call(() => { if (w) w.textContent = word; if (dot) dot.setAttribute("fill", color); }, null, at);
    tl.set(env, { opacity: 1 }, at);
    tl.fromTo(p, { t: from }, { t: to, duration: 1.4, ease: "power1.inOut", onUpdate: place, onStart: place }, at);
    tl.set(env, { opacity: 0 }, at + 1.4);
  };
  const tl = gsap.timeline({ repeat: -1, repeatDelay: 0.6, paused: true });
  tl.set([line, under], { strokeDasharray: len, strokeDashoffset: len }, 0);
  tl.set(env, { opacity: 0 }, 0);
  tl.to([line, under], { strokeDashoffset: 0, duration: 0.9, ease: "power2.out" }, 0.4);
  travel(tl, 1.6, 0, 1, wordIn, "#E24E1B");
  tl.fromTo(bots[1], { scale: 1.1 }, { scale: 1.2, duration: 0.25, yoyo: true, repeat: 1, transformOrigin: "50% 50%" }, 3.0);
  travel(tl, 3.8, 1, 0, wordOut, "#15624A");
  tl.fromTo(bots[0], { scale: 1.1 }, { scale: 1.2, duration: 0.25, yoyo: true, repeat: 1, transformOrigin: "50% 50%" }, 5.2);
  tl.to({}, { duration: 1.6 }, 5.8);
  ScrollTrigger.create({
    trigger: stage, start: "top 85%", end: "bottom 10%",
    onEnter: () => tl.play(), onEnterBack: () => tl.play(),
    onLeave: () => tl.pause(), onLeaveBack: () => tl.pause(),
  });
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) tl.pause();
    else if (ScrollTrigger.isInViewport && ScrollTrigger.isInViewport(stage)) tl.play();
  });
}

/* ---- public entry: one service bot, many private lines (v0.6.0) --------- */
// A service bot on its owner's computer; three other bots, each on its own
// computer, join through one public link, each on a separate line through the
// cloud; generic messages (Book/Ask -> Confirm) travel on their own lines at
// once; then one line is closed and the others keep going.
// Static markup (all lines, all guests) is the no-JS / reduced-motion state.
function wireMany() {
  const stage = document.querySelector("[data-many]");
  if (!stage) return;
  const $ = (id) => stage.querySelector("#" + id);
  const cap = stage.querySelector("#many-cap");
  const caps = [0, 1, 2, 3, 4].map((i) => stage.getAttribute("data-cap" + i) || "");
  const lines = [0, 1, 2].map((i) => $("pe-line" + i));
  const unders = [0, 1, 2].map((i) => stage.querySelector('.pe-under[data-i="' + i + '"]'));
  const guests = [0, 1, 2].map((i) => $("pe-g" + i));
  const envs = [0, 1, 2].map((i) => $("pe-env" + i));
  const note = $("pe-note");
  const noteT = $("pe-note-t");
  const xMark = $("pe-x");
  const svc = $("pe-svc");
  if (reduceMotion || !cap || lines.some((l) => !l || !l.getTotalLength) ||
      unders.some((u) => !u) || guests.some((g) => !g) || envs.some((e) => !e) ||
      !note || !noteT || !xMark || !svc) return;

  const names = guests.map((g) => (g.querySelector("text") || {}).textContent || "");
  const words = envs.map((e) => e.querySelector("text"));
  const wordIn = words.map((w) => (w && w.getAttribute("data-in")) || "");
  const wordOut = stage.getAttribute("data-out") || "";
  const notePrefix = noteT.getAttribute("data-pre") || "";
  const lens = lines.map((l) => l.getTotalLength());
  // zh captions break only between clauses (after ，。), never mid-phrase
  const say = (i) => () => {
    if (!zh) { cap.textContent = caps[i]; return; }
    const parts = caps[i].match(/[^，。,.]+[，。,.]?\s*/g) || [caps[i]];
    cap.replaceChildren(...parts.flatMap((p) => {
      const sp = document.createElement("span");
      sp.className = "nowrap";
      sp.textContent = p.trim();
      return /\s$/.test(p) ? [sp, document.createTextNode(" ")] : [sp];
    }));
  };
  const setNote = (i) => () => { noteT.textContent = notePrefix + (zh ? "" : " ") + names[i]; };

  // message bubble travel along line i; from=1 starts at the guest end
  const travel = (tl, i, at, from, to, dur) => {
    const env = envs[i], line = lines[i], len = lens[i], p = { t: from };
    const w = words[i], txt = from > to ? wordIn[i] : wordOut;
    if (w) tl.call(() => { w.textContent = txt; }, null, at);
    const place = () => {
      // stop short of the computers so the bubble never covers a name
      const pt = line.getPointAtLength(len * (0.1 + 0.76 * p.t));
      gsap.set(env, { x: pt.x, y: pt.y });
    };
    tl.set(env, { opacity: 1 }, at);
    tl.fromTo(p, { t: from }, { t: to, duration: dur, ease: "power1.inOut", onUpdate: place, onStart: place }, at);
    tl.set(env, { opacity: 0 }, at + dur);
  };

  const tl = gsap.timeline({ repeat: -1, repeatDelay: 0.4, paused: true });
  // 0 — clean state
  tl.call(say(0), null, 0);
  lines.forEach((l, i) => {
    tl.set([l, unders[i]], { strokeDasharray: lens[i], strokeDashoffset: lens[i] }, 0);
    tl.set(l, { attr: { stroke: l.getAttribute("stroke") } }, 0);
  });
  tl.set(guests, { opacity: 0, scale: 0.7, transformOrigin: "50% 50%" }, 0);
  tl.set([note, xMark], { opacity: 0 }, 0);
  tl.set(xMark, { scale: 0.3, transformOrigin: "50% 50%" }, 0);
  tl.set(envs, { opacity: 0 }, 0);
  tl.fromTo(svc, { scale: 1.25 }, { scale: 1.36, duration: 0.35, yoyo: true, repeat: 1, ease: "power1.inOut", transformOrigin: "50% 50%" }, 0.4);
  // 1 — bots join through the one public link (a short note on each join)
  tl.call(say(1), null, 1.8);
  [0, 1, 2].forEach((i) => {
    const at = 2.0 + i * 1.0;
    tl.to(guests[i], { opacity: 1, scale: 1, duration: 0.45, ease: "back.out(2)" }, at);
    tl.to([lines[i], unders[i]], { strokeDashoffset: 0, duration: 0.7, ease: "power2.out" }, at + 0.15);
    tl.call(setNote(i), null, at + 0.5);
    tl.to(note, { opacity: 1, duration: 0.2 }, at + 0.5);
  });
  tl.to(note, { opacity: 0, duration: 0.3 }, 5.6);
  // 2 — each bot has a separate line
  tl.call(say(2), null, 5.4);
  [0, 1, 2].forEach((i) => {
    tl.to(lines[i], { attr: { "stroke-width": 9 }, duration: 0.22, yoyo: true, repeat: 1 }, 5.7 + i * 0.4);
  });
  // 3 — messages come in at once, each on its own line; replies go back
  tl.call(say(3), null, 7.6);
  [0, 1, 2].forEach((i) => travel(tl, i, 7.9 + i * 0.18, 1, 0, 1.3));
  [0, 1, 2].forEach((i) => travel(tl, i, 9.6 + i * 0.18, 0, 1, 1.3));
  // 4 — one suspicious line is closed; the others keep working
  tl.call(say(4), null, 11.6);
  tl.to(xMark, { opacity: 1, scale: 1, duration: 0.4, ease: "back.out(2.4)" }, 11.9);
  tl.to(lines[2], { attr: { stroke: "#C9BDA6" }, duration: 0.4 }, 12.1);
  tl.to(guests[2], { opacity: 0.35, duration: 0.4 }, 12.1);
  [0, 1].forEach((i) => travel(tl, i, 12.8 + i * 0.2, 1, 0, 1.2));
  tl.to({}, { duration: 1.8 }, 14.2);

  ScrollTrigger.create({
    trigger: stage,
    start: "top 85%",
    end: "bottom 10%",
    onEnter: () => tl.play(),
    onEnterBack: () => tl.play(),
    onLeave: () => tl.pause(),
    onLeaveBack: () => tl.pause(),
  });
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) tl.pause();
    else if (ScrollTrigger.isInViewport && ScrollTrigger.isInViewport(stage)) tl.play();
  });
}

/* ---- hero explainer (GSAP timeline over inline SVG) ---------------------- */
// Six captioned steps on a "two computers + cloud" scene: a Grok Bot on each
// person's computer, both linked to the botobot.app cloud -> Amy briefs her bot
// -> the sealed envelope leaves her computer up to the cloud -> the cloud only
// relays it (can't open it) and it drops into Bob's computer -> Bob's bot reads
// it and email/payments wait for approval -> the reply goes back the same way.
// Captions live in the DOM (aria-live). Step dots show per-step progress and
// jump on click; a toggle and a replay button control playback. Pauses when
// offscreen or when the tab is hidden. If any piece is missing, the static
// storyboard (CSS fallback) stays and we bail. Under prefers-reduced-motion it
// still plays (the owner wants the story to move), but slower and without the
// idle bob amplitude.
function wireExplainer() {
  const scene = document.querySelector(".hero-explain");
  if (!scene) return;
  const svg = scene.querySelector(".hero-scene svg");
  const cap = document.getElementById("hero-cap");
  const dots = Array.prototype.slice.call(scene.querySelectorAll(".dot"));
  const toggle = document.getElementById("hero-toggle");
  const replay = document.getElementById("hero-replay");
  if (!svg || !cap || dots.length !== 6 || !toggle || !replay) return;

  const el = (id) => svg.querySelector("#" + id);
  const env = el("he-env"),
    lock = el("he-lock"),
    brief = el("he-brief"),
    eye = el("he-eye"),
    recv = el("he-recv"),
    check = el("he-check"),
    back = el("he-back"),
    scrL = el("he-scrL"),
    scrR = el("he-scrR"),
    lineL = el("he-lineL"),
    lineR = el("he-lineR");
  if (!env || !lock || !brief || !eye || !recv || !check || !back || !scrL || !scrR || !lineL || !lineR) return;
  const botL = el("he-botL"),
    botR = el("he-botR");

  // this browser can run the explainer: reveal it, hide the storyboard (CSS)
  root.classList.add("hero-on");

  // envelope waypoints: on each screen (at the bot) and in the cloud. The
  // control points match the drawn link paths (M140 244 Q150 30 260 66).
  const S = { x: 140, y: 244 };
  const HUB = { x: 260, y: 66 };
  const E = { x: 380, y: 244 };
  const C1 = { x: 150, y: 30 };
  const C2 = { x: 370, y: 30 };
  const scrOff = { "fill-opacity": 0.1, "stroke-opacity": 0.4 };
  const scrOn = { "fill-opacity": 0.24, "stroke-opacity": 0.95 };
  const ENV_SCALE = 0.78;

  // quadratic-bezier travel for the envelope, added onto the timeline
  const bez = (node, p0, c, p1, dur) => {
    const o = { t: 0 };
    return gsap.to(o, {
      t: 1,
      duration: dur,
      ease: "power1.inOut",
      onUpdate() {
        const t = o.t,
          mt = 1 - t;
        gsap.set(node, {
          x: mt * mt * p0.x + 2 * mt * t * c.x + t * t * p1.x,
          y: mt * mt * p0.y + 2 * mt * t * c.y + t * t * p1.y,
        });
      },
    });
  };
  // dashes flowing along a link, in the travel direction (dir 1 = up to cloud)
  const flow = (line, dir, at, dur) =>
    tl.fromTo(line, { attr: { "stroke-dashoffset": 0 } }, { attr: { "stroke-dashoffset": dir * -52 }, duration: dur, ease: "none" }, at);

  const tl = gsap.timeline({ repeat: -1, paused: true });

  // time 0 — clean step-1 state (also re-runs on every loop)
  tl.set(env, { opacity: 0, x: S.x, y: S.y, scale: ENV_SCALE, transformOrigin: "50% 50%" });
  tl.set(lock, { scale: 1, rotation: 0, transformOrigin: "50% 50%" });
  tl.set([brief, eye, recv, check, back], { opacity: 0, scale: 0, transformOrigin: "50% 50%" });
  tl.set([scrL, scrR], { attr: scrOff });

  // 1 — a Grok Bot on each computer, both linked through the cloud
  tl.to([scrL, scrR], { attr: scrOn, duration: 0.5 }, 0.15);
  flow(lineL, 1, 0.2, 2.0);
  flow(lineR, 1, 0.2, 2.0);
  tl.to([scrL, scrR], { attr: scrOff, duration: 0.5 }, 2.0);

  // 2 — Amy briefs the bot on her computer
  tl.to(scrL, { attr: scrOn, duration: 0.5 }, 2.7);
  tl.to(brief, { opacity: 1, scale: 1, duration: 0.5, ease: "back.out(2)" }, 2.85);
  tl.to(brief, { opacity: 0, scale: 0.6, duration: 0.4, ease: "power1.in" }, 4.3);

  // 3 — sealed (padlock snaps on), then up from Amy's computer to the cloud
  tl.set(env, { x: S.x, y: S.y }, 4.8);
  tl.to(env, { opacity: 1, duration: 0.3 }, 4.85);
  tl.fromTo(lock, { scale: 0 }, { scale: 1, duration: 0.45, ease: "back.out(2.4)", transformOrigin: "50% 50%" }, 5.0);
  tl.to(scrL, { attr: scrOff, duration: 0.5 }, 5.6);
  tl.add(bez(env, S, C1, HUB, 1.4), 5.75);
  flow(lineL, 1, 5.75, 1.4);

  // 4 — the cloud only relays it (can't open it), then down to Bob's computer
  tl.to(eye, { opacity: 1, scale: 1, duration: 0.45, ease: "back.out(2)" }, 7.45);
  tl.to(lock, { rotation: -10, duration: 0.1, yoyo: true, repeat: 5 }, 7.5);
  tl.set(lock, { rotation: 0 }, 8.15);
  tl.to(eye, { opacity: 0, scale: 0.6, duration: 0.4 }, 8.3);
  tl.add(bez(env, HUB, C2, E, 1.4), 8.5);
  flow(lineR, -1, 8.5, 1.4);

  // 5 — Bob's bot opens and reads it; email and payments wait for approval
  tl.to(scrR, { attr: scrOn, duration: 0.5 }, 9.8);
  tl.to(lock, { scale: 0, duration: 0.35, ease: "back.in(2)" }, 10.05);
  tl.to(env, { opacity: 0, duration: 0.35 }, 10.2);
  tl.to(recv, { opacity: 1, scale: 1, duration: 0.5, ease: "back.out(2)" }, 10.3);
  tl.to(check, { opacity: 1, scale: 1, duration: 0.5, ease: "back.out(2.2)" }, 11.0);
  tl.to([recv, check], { opacity: 0, duration: 0.45 }, 12.4);
  tl.to(scrR, { attr: scrOff, duration: 0.5 }, 12.5);

  // 6 — the reply goes back the same way: Bob's computer -> cloud -> Amy's
  tl.set(env, { x: E.x, y: E.y }, 12.9);
  tl.set(lock, { scale: 1, rotation: 0 }, 12.9);
  tl.to(env, { opacity: 1, duration: 0.3 }, 12.95);
  tl.add(bez(env, E, C2, HUB, 1.2), 13.3);
  flow(lineR, 1, 13.3, 1.2);
  tl.add(bez(env, HUB, C1, S, 1.2), 14.55);
  flow(lineL, -1, 14.55, 1.2);
  tl.to(scrL, { attr: scrOn, duration: 0.5 }, 15.55);
  tl.to(lock, { scale: 0, duration: 0.35, ease: "back.in(2)" }, 15.8);
  tl.to(env, { opacity: 0, duration: 0.35 }, 15.95);
  tl.to(back, { opacity: 1, scale: 1, duration: 0.5, ease: "back.out(2)" }, 16.05);
  tl.to(back, { opacity: 0, duration: 0.45 }, 17.5);
  tl.to(scrL, { attr: scrOff, duration: 0.5 }, 17.55);
  tl.to({}, { duration: 0.45 });

  if (reduceMotion) tl.timeScale(0.7);

  // idle "breathing" bob on both assistants so the scene is alive from frame 1
  const bob = [botL, botR]
    .filter(Boolean)
    .map((b, k) =>
      gsap.to(b, {
        y: reduceMotion ? -2 : -6,
        duration: 1.3,
        delay: k * 0.65,
        yoyo: true,
        repeat: -1,
        ease: "sine.inOut",
        paused: true,
      })
    );

  const stepTimes = [0, 2.6, 4.75, 7.4, 9.75, 12.85];
  const total = tl.duration();

  // Captions break only between clauses (after ，；, ;), never mid-phrase.
  const setCap = (text) => {
    cap.textContent = "";
    const parts = text.split(/(?:(，|；)|([,;]) )/);
    for (let k = 0; k < parts.length; k += 3) {
      const span = document.createElement("span");
      span.className = "nowrap";
      span.textContent = parts[k] + (parts[k + 1] || parts[k + 2] || "");
      cap.appendChild(span);
    }
  };

  let cur = -1;
  const paint = () => {
    const t = tl.time();
    let i = 0;
    for (let k = 0; k < stepTimes.length; k++) {
      if (t + 1e-4 >= stepTimes[k]) i = k;
    }
    const segStart = stepTimes[i];
    const segEnd = i + 1 < stepTimes.length ? stepTimes[i + 1] : total;
    const p = segEnd > segStart ? Math.min(1, Math.max(0, (t - segStart) / (segEnd - segStart))) : 0;
    dots.forEach((d, k) => {
      const f = d.querySelector(".dot-fill");
      if (f) f.style.setProperty("--p", k < i ? 1 : k === i ? p : 0);
      d.classList.toggle("on", k === i);
      if (k === i) d.setAttribute("aria-current", "true");
      else d.removeAttribute("aria-current");
    });
    if (i !== cur) {
      cur = i;
      setCap(dots[i].getAttribute("data-cap") || "");
    }
  };
  tl.eventCallback("onUpdate", paint);

  const labels = zh
    ? { pause: "暫停動畫", play: "播放動畫" }
    : { pause: "Pause animation", play: "Play animation" };
  const reflect = () => {
    const playing = !tl.paused();
    toggle.setAttribute("data-playing", playing ? "1" : "0");
    toggle.setAttribute("aria-label", playing ? labels.pause : labels.play);
  };

  let userPaused = false;
  // autoplay starts only once the IntersectionObserver reports the scene in view
  // Visible right now? (Covers the hero being in view on load, before the
  // observer's first callback, and browsers without IntersectionObserver.)
  const inView = () => {
    const r = scene.getBoundingClientRect();
    return r.bottom > 0 && r.top < (window.innerHeight || document.documentElement.clientHeight);
  };
  let onScreen = !("IntersectionObserver" in window) || inView();
  const sync = () => {
    const run = onScreen && !document.hidden && !userPaused;
    if (run) tl.play();
    else tl.pause();
    bob.forEach((b) => (run ? b.play() : b.pause()));
    reflect();
  };

  toggle.addEventListener("click", () => {
    userPaused = !tl.paused();
    sync();
  });
  replay.addEventListener("click", () => {
    userPaused = false;
    tl.restart();
    onScreen = onScreen || inView();
    sync();
    paint();
  });
  dots.forEach((d) => {
    d.addEventListener("click", () => {
      userPaused = false;
      // suppressEvents=false: the envelope's bezier legs move it from onUpdate,
      // so a jump must run them to land the envelope where that step expects.
      tl.seek(stepTimes[+d.getAttribute("data-step")], false);
      onScreen = onScreen || inView();
      sync();
      paint();
    });
  });

  if ("IntersectionObserver" in window) {
    new IntersectionObserver(
      (entries) => {
        onScreen = entries[0].isIntersecting;
        sync();
      },
      { threshold: 0 }
    ).observe(scene);
  }
  document.addEventListener("visibilitychange", sync);

  window.addEventListener("load", () => {
    if (inView()) {
      onScreen = true;
      sync();
    }
  });

  paint();
  reflect();
  sync();
}

/* ---- wordmark intro ------------------------------------------------------ */
function wireWordmark() {
  if (reduceMotion) return;
  const mark = document.querySelector(".site-head .brand-name");
  if (!mark) return;
  const b1 = mark.querySelector(".wm-b1");
  const o = mark.querySelector(".wm-o");
  const b2 = mark.querySelector(".wm-b2");
  if (!b1 || !o || !b2) return;
  gsap
    .timeline({ delay: 0.35 })
    .from(b1, { x: -14, opacity: 0, duration: 0.5, ease: "power3.out" })
    .from(b2, { x: 14, opacity: 0, duration: 0.5, ease: "power3.out" }, "<")
    .from(o, { scale: 0, opacity: 0, duration: 0.45, ease: "back.out(3)" }, "-=0.12");
}

/* ---- chat demo (#how) ---------------------------------------------------- */
// Two chat windows that replay the real setup chat with fake values: the owner
// asks, the bot sets up (typing dots, then bubbles), the routine chip, the
// invite card line by line, then the other person's bot joins. Static markup is
// the no-JS fallback (everything visible). Loops while in view; pauses when
// offscreen or when the tab is hidden. Space is reserved (autoAlpha keeps
// layout), so nothing jumps while it plays.
function wireChatDemo() {
  const stage = document.querySelector("[data-chatdemo]");
  if (!stage) return;
  // DOM order, except rows marked data-cd-last play at the very end (the
  // admin's join notice shows after the other side has joined).
  const all = Array.from(stage.querySelectorAll("[data-cd]"));
  const items = all.filter((el) => !("cdLast" in el.dataset)).concat(all.filter((el) => "cdLast" in el.dataset));
  if (!items.length) return;

  const dotsFor = (row) => {
    const d = document.createElement("span");
    d.className = "cd-typing";
    d.setAttribute("aria-hidden", "true");
    d.innerHTML = "<i></i><i></i><i></i>";
    row.prepend(d);
    return d;
  };

  const tl = gsap.timeline({ repeat: -1, repeatDelay: 0.4, paused: true });
  tl.set(items, { autoAlpha: 0, y: 0, scale: 1 });
  tl.set(stage.querySelectorAll("[data-cd-line]"), { autoAlpha: 0 });
  let t = 0.5;
  let prevWin = null;
  items.forEach((el) => {
    const win = el.closest(".cd-win");
    if (prevWin && win !== prevWin) t += 1.0; // pause before the other side
    prevWin = win;
    const kind = el.dataset.cd;
    const bubble = el.querySelector(".cd-b") || el;
    if (kind === "me") {
      tl.fromTo(el, { autoAlpha: 0, y: 14, scale: 0.97 }, { autoAlpha: 1, y: 0, scale: 1, duration: 0.45, ease: "back.out(1.6)", transformOrigin: "100% 100%" }, t);
      t += 0.9;
    } else if (kind === "chip") {
      tl.fromTo(el, { autoAlpha: 0, y: 6 }, { autoAlpha: 1, y: 0, duration: 0.4, ease: "power2.out" }, t);
      t += 0.75;
    } else {
      const dots = dotsFor(el);
      const dotEls = dots.querySelectorAll("i");
      tl.set(dots, { autoAlpha: 1 }, t);
      tl.fromTo(dotEls, { y: 0 }, { y: -4, duration: 0.25, stagger: 0.12, yoyo: true, repeat: 2, ease: "sine.inOut" }, t);
      t += 0.85;
      tl.set(dots, { autoAlpha: 0 }, t);
      tl.fromTo(el, { autoAlpha: 0, y: 12 }, { autoAlpha: 1, y: 0, duration: 0.4, ease: "power2.out" }, t);
      const parts = kind === "card" ? el.querySelectorAll("[data-cd-line]") : bubble.querySelectorAll("p");
      if (kind === "card") {
        tl.to(parts, { autoAlpha: 1, duration: 0.25, stagger: 0.25 }, t + 0.3);
        t += 0.3 + parts.length * 0.25 + 0.7;
      } else if (parts.length > 1) {
        tl.from(parts, { autoAlpha: 0, duration: 0.35, stagger: 0.75 }, t);
        t += parts.length * 0.75 + 0.5;
      } else {
        t += 1.1;
      }
    }
  });
  tl.to({}, { duration: 4.5 }, t);
  tl.to(items, { autoAlpha: 0, duration: 0.45, ease: "power1.in" });

  // Hidden until the first play, so it never flashes the end state.
  gsap.set(items, { autoAlpha: 0 });
  ScrollTrigger.create({
    trigger: stage,
    start: "top 85%",
    end: "bottom 15%",
    onToggle: (self) => (self.isActive ? tl.play() : tl.pause()),
  });
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) tl.pause();
    else if (ScrollTrigger.isInViewport && ScrollTrigger.isInViewport(stage, 0.1)) tl.play();
  });
}

wireCopy();
wireStickyCTA();
wireWordmark();
wireReveals();
wireStory();
wirePrivacy();
wireExplainer();
wireChatDemo();
wireOne();
wireMany();

// Recompute triggers once fonts settle (CJK metrics shift layout).
if (document.fonts && document.fonts.ready) {
  document.fonts.ready.then(() => ScrollTrigger.refresh());
}
