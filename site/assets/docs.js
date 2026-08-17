/* scrollspy: highlight the sidebar section link for the heading in view */
(function () {
  var links = Array.prototype.slice.call(document.querySelectorAll(".side .sections a"));
  if (!links.length) return;
  var pairs = links
    .map(function (a) {
      var el = document.getElementById(a.getAttribute("href").slice(1));
      return el ? [el, a] : null;
    })
    .filter(Boolean);
  function spy() {
    var cur = null;
    for (var i = 0; i < pairs.length; i++) {
      if (pairs[i][0].getBoundingClientRect().top <= 96) cur = pairs[i][1];
    }
    links.forEach(function (a) { a.classList.toggle("active", a === cur); });
  }
  addEventListener("scroll", spy, { passive: true });
  addEventListener("hashchange", spy);
  spy();
})();
