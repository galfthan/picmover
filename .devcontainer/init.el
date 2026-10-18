;; Emacs setup for the picmover dev container. go-mode is installed into
;; ~/.emacs.d/elpa when the image is built, so no network is needed here.
(require 'package)
(add-to-list 'package-archives '("melpa" . "https://melpa.org/packages/") t)
(package-initialize)
(unless (package-installed-p 'go-mode)
  (package-refresh-contents)
  (package-install 'go-mode))
(global-font-lock-mode t)
(add-to-list 'auto-mode-alist '("\\.go\\'" . go-mode))
