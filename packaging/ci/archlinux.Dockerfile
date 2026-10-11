FROM archlinux:base-devel

RUN pacman -Syu --needed --noconfirm \
        ccache \
        cmake \
        curl \
        desktop-file-utils \
        dpkg \
        fakeroot \
        file \
        git \
        go \
        libgit2 \
        ninja \
        patchelf \
        pkgconf \
        python \
        qt6-base \
        qt6-declarative \
        qt6-imageformats \
        qt6-multimedia \
        qt6-svg \
        qtkeychain-qt6 \
        qtermwidget \
        syntax-highlighting \
    && pacman -Scc --noconfirm
