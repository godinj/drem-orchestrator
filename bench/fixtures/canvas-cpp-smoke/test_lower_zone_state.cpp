#include "LowerZoneState.h"

#include <cassert>

int main()
{
    dc::LowerZoneState state;
    assert (state.getArrangementShare() == dc::LowerZoneDefaults::defaultArrangementShare);

    state.setArrangementShare (-1.0f);
    assert (state.getArrangementShare() == dc::LowerZoneDefaults::minArrangementShare);

    state.setArrangementShare (2.0f);
    assert (state.getArrangementShare() == dc::LowerZoneDefaults::maxArrangementShare);
}
