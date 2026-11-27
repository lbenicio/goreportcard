(function () {
  var root = document.documentElement;
  var toggle = document.querySelector(".theme-toggle");
  var media = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;

  function updateButton(theme) {
    if (!toggle) {
      return;
    }

    var nextTheme = theme === "dark" ? "light" : "dark";
    var icon = toggle.querySelector("i");
    var label = toggle.querySelector(".theme-label");
    if (icon) {
      icon.className = theme === "dark" ? "fa fa-sun-o" : "fa fa-moon-o";
    }
    if (label) {
      label.textContent = nextTheme === "dark" ? "Dark mode" : "Light mode";
    }
    toggle.setAttribute("aria-label", "Switch to " + nextTheme + " mode");
    toggle.setAttribute("title", "Switch to " + nextTheme + " mode");
  }

  function setTheme(theme, preference) {
    root.setAttribute("data-theme", theme);
    root.setAttribute("data-theme-preference", preference);
    updateButton(theme);
  }

  updateButton(root.getAttribute("data-theme") || "light");

  if (toggle) {
    toggle.addEventListener("click", function () {
      var nextTheme = root.getAttribute("data-theme") === "dark" ? "light" : "dark";
      setTheme(nextTheme, "user");
      try {
        window.localStorage.setItem("goreportcard-theme", nextTheme);
      } catch (error) {}
    });
  }

  if (media) {
    var applySystemPreference = function (event) {
      if (root.getAttribute("data-theme-preference") === "system") {
        setTheme(event.matches ? "dark" : "light", "system");
      }
    };
    if (media.addEventListener) {
      media.addEventListener("change", applySystemPreference);
    } else if (media.addListener) {
      media.addListener(applySystemPreference);
    }
  }
})();
