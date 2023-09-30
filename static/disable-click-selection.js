const elements = document.querySelectorAll("[data-disable-click-selection]");
for (let i = 0; i < elements.length; i++) {
    elements[i].addEventListener('mousedown', function(event) {
        // https://stackoverflow.com/a/43321596
        if (event.detail > 1) {
            event.preventDefault();
        }
    });
}
