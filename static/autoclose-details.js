// https://til.simonwillison.net/javascript/dropdown-menu-with-details-summary
document.body.parentElement.addEventListener('click', (ev) => {
    /* Close any open details elements that this click is outside of */
    var target = ev.target;
    var detailsClickedWithin = null;
    while (target && target.tagName !== 'DETAILS') {
        target = target.parentNode;
    }
    if (target && target.tagName === 'DETAILS') {
        detailsClickedWithin = target;
    }
    Array.from(document.getElementsByTagName('details')).filter(
        (details) => details.open && details !== detailsClickedWithin && !details.hasAttribute("data-dont-autoclose-details")
    ).forEach(details => details.open = false);
});
