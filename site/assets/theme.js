/* The theme toggle. The boot script in each page's head has already
   set data-theme before first paint; this only flips it and remembers.
   Light is the default — the stored value is only ever read back as
   "dark" or "light", anything else falls through to light. */
(function () {
  var btn = document.getElementById("mode");
  if (!btn) return;
  function paint() {
    var dark = document.documentElement.dataset.theme === "dark";
    btn.textContent = dark ? "[ light ]" : "[ dark ]";
    btn.setAttribute(
      "aria-label",
      dark ? "Switch to light theme" : "Switch to dark theme"
    );
  }
  btn.addEventListener("click", function () {
    var next =
      document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    try {
      localStorage.setItem("stormlight-theme", next);
    } catch (e) {
      /* a theme that cannot persist still applies for the visit */
    }
    paint();
  });
  paint();
})();
